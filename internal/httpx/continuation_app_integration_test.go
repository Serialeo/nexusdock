package httpx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	protocol "github.com/Serialeo/agentdock-protocol"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uvwt/nexusdock/internal/auth"
	projectstore "github.com/uvwt/nexusdock/internal/project"
)

type continuationAppTransport struct{ token string }

func (transport continuationAppTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(request)
}

// 本测试执行真正 resources/read 返回的 HTML：只有宿主 iframe 外壳被模拟，
// tools/call 均经过带 MCP Token 的 HTTP /mcp 与真实 SQLite Store。
// ui/message 被接收后用同一 HTTP MCP 会话 consume/settle，不能据此声称 ChatGPT 已接入。
func TestContinuationAppHTTPStoreRoundTrip(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute the MCP App; CI installs Node")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	server, projects, project, deployment, _, closeNode := prepareOnlineProjectMCPTest(t)
	defer closeNode()
	server.mcpToken, err = auth.NewMCPTokenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server.setMCPAppsEnabled(true)
	device, err := server.agentDock.Get(ctx, deployment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.agentDock.UpdateHello(ctx, deployment.NodeID, protocol.Hello{DeviceID: device.DeviceID, ProtocolVersion: protocol.ConnectionProtocolVersion, UIResources: []protocol.UIResourceCapability{}, Capabilities: []string{}, Tools: []protocol.ToolDescriptor{}, BridgeCapabilities: []string{protocol.CommandOutcomesCapability}}); err != nil {
		t.Fatal(err)
	}
	const owner = "mcp:dedicated-token"
	session, _, err := projects.BeginWorkSession(ctx, owner, project.ID, "app-http-roundtrip", "digest", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = projects.UpdateWorkSessionContext(ctx, owner, session.ID, project.Revision, protocol.WorkSessionReady, "app-context")
	if err != nil {
		t.Fatal(err)
	}
	target, err := projects.PutWorkTarget(ctx, owner, projectstore.WorkTarget{Target: protocol.WorkTarget{ID: "app-target", WorkSessionID: session.ID, ProjectID: project.ID, DeploymentID: deployment.ID, NodeID: deployment.NodeID, CWDRel: ".", DeploymentRevision: deployment.AppliedRevision, ContextRevision: "app-context", Status: protocol.TargetReady, Permissions: deployment.Permissions}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	exitCode := 0
	outcome := protocol.CommandOutcome{EventID: "app-event", CommandSessionID: "app-command", ExecutionContext: protocol.ExecutionContext{WorkSessionID: session.ID, TargetID: target.Target.ID, ProjectID: project.ID, DeploymentID: deployment.ID, DeploymentRevision: deployment.AppliedRevision, ContextRevision: "app-context"}, State: protocol.CommandOutcomeCompleted, ExitCode: &exitCode, Stdout: "durable app output", StartedAt: now, FinishedAt: now, UpdatedAt: now, PendingReport: true}
	if _, err = projects.RecordCommandOutcomes(ctx, deployment.NodeID, []protocol.CommandOutcome{outcome}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "continuation-app-integration", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: continuationAppTransport{token: server.mcpToken.Token()}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	call := func(name string, args any) *mcpsdk.CallToolResult {
		t.Helper()
		result, callErr := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if callErr != nil || result.IsError {
			t.Fatalf("%s: %#v %v", name, result, callErr)
		}
		return result
	}
	call("work_continuation", protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "enable", Confirmed: true, MaxRounds: 3, MaxFailures: 2})
	presented := call("present_work_continuation", protocol.PresentWorkContinuationInput{WorkSessionID: session.ID})
	call("work_continuation", protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "await", Sources: []protocol.ContinuationSource{{TargetID: target.Target.ID, CommandSessionID: outcome.CommandSessionID}}})
	resource, err := clientSession.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: protocol.WorkContinuationUIResourceURI})
	if err != nil || len(resource.Contents) != 1 || resource.Contents[0].Text == "" {
		t.Fatalf("controller resource: %#v %v", resource, err)
	}
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, node, "-e", continuationAppHostFixture)
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	encoder := json.NewEncoder(stdin)
	if err = encoder.Encode(map[string]any{"html": resource.Contents[0].Text, "presentation": presented}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	sentMessages, prepares, finishes := 0, 0, 0
	var consumedWake string
	for scanner.Scan() {
		var request struct {
			ID       int             `json:"id"`
			Method   string          `json:"method"`
			Params   json.RawMessage `json:"params"`
			Complete bool            `json:"complete"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if request.Complete {
			break
		}
		var response any
		switch request.Method {
		case "ui/initialize":
			response = map[string]any{"protocolVersion": "2026-01-26", "hostCapabilities": map[string]any{"serverTools": map[string]any{}, "message": map[string]any{"text": map[string]any{}}}, "hostContext": map[string]any{"locale": "en"}}
		case "tools/call":
			var params mcpsdk.CallToolParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Name == "work_wake_prepare" {
				prepares++
			}
			if params.Name == "work_wake_finish" {
				finishes++
			}
			response = call(params.Name, params.Arguments)
		case "ui/message":
			sentMessages++
			var params struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if err = json.Unmarshal(request.Params, &params); err != nil || params.Role != "user" || len(params.Content) != 1 || params.Content[0].Type != "text" {
				t.Fatalf("invalid public ui/message: %s %v", request.Params, err)
			}
			const prefix = "Call consume_work_wake with exactly this continuation envelope: "
			text := params.Content[0].Text
			if !strings.HasPrefix(text, prefix) {
				t.Fatal("controller changed server message")
			}
			var envelope protocol.ResumeEnvelope
			if err = json.Unmarshal([]byte(strings.TrimPrefix(text, prefix)), &envelope); err != nil {
				t.Fatal(err)
			}
			expected, err := envelope.AutomaticMessage()
			if err != nil || expected != text {
				t.Fatal("automatic_message was not forwarded verbatim")
			}
			consumed := call("consume_work_wake", envelope)
			encoded, _ := json.Marshal(consumed.StructuredContent)
			var result protocol.WorkContinuationResult
			if err = json.Unmarshal(encoded, &result); err != nil || len(result.Outcomes) != 1 || result.Outcomes[0].Stdout != "durable app output" {
				t.Fatalf("consume did not return authoritative durable output: %s %v", encoded, err)
			}
			consumedWake = envelope.WakeID
			// 刻意先 consume+settle，再确认 ui/message；迟到 finish 必须保持结算终态。
			call("work_continuation", protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "settle", WakeID: envelope.WakeID, Checkpoint: "verified durable output through HTTP App roundtrip"})
			response = map[string]any{}
		default:
			t.Fatalf("unexpected host operation: %s", request.Method)
		}
		if err = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": response}); err != nil {
			t.Fatal(err)
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err = command.Wait(); err != nil {
		t.Fatalf("App host fixture: %v: %s", err, stderr.String())
	}
	if sentMessages != 1 || prepares != 1 || finishes != 1 || consumedWake == "" {
		t.Fatalf("incomplete App cycle: messages=%d prepares=%d finishes=%d stderr=%s", sentMessages, prepares, finishes, stderr.String())
	}
	result, err := projects.ControlContinuation(ctx, owner, protocol.WorkContinuationInput{WorkSessionID: session.ID, Action: "status"})
	if err != nil || result.Wake != nil || result.State.Checkpoint != "verified durable output through HTTP App roundtrip" {
		t.Fatalf("late finish undid settlement: %#v %v", result, err)
	}
}

const continuationAppHostFixture = `
const vm=require('node:vm'), readline=require('node:readline');
const lines=readline.createInterface({input:process.stdin});
const events={},elements={},requests=new Map();
let setup=false,initial;
const emit=value=>process.stdout.write(JSON.stringify(value)+'\n');
const parent={postMessage:request=>{
 if(request.id){requests.set(request.id,request);emit(request)}
 else if(request.method==='ui/notifications/initialized'){
  events.message({source:parent,data:{jsonrpc:'2.0',method:'ui/notifications/tool-result',params:initial.presentation}});
  setImmediate(()=>elements.enable.click());
 }
}};
lines.on('line',line=>{
 const message=JSON.parse(line);
 if(!setup){
  setup=true;initial=message;
  for(const id of ['title','status','scope','budget','detail','enable','pause'])elements[id]={disabled:false,textContent:'',addEventListener:(event,handler)=>elements[id][event]=handler};
  const document={visibilityState:'visible',documentElement:{style:{},getBoundingClientRect:()=>({height:240})},getElementById:id=>elements[id],addEventListener:()=>{}};
  const script=initial.html.match(/<script>([\s\S]*?)<\/script>/)[1];
  vm.runInNewContext(script,{window:{parent,addEventListener:(name,handler)=>events[name]=handler},document,navigator:{language:'en'},crypto:require('node:crypto').webcrypto,Uint8Array,Date,setTimeout,clearTimeout});
 }else{
  const request=requests.get(message.id);requests.delete(message.id);
  events.message({source:parent,data:message});
  if(request?.params?.name==='work_wake_finish')setImmediate(()=>{emit({complete:true});process.exit(0)});
 }
});
`
