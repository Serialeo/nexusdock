const defaultMCPTimeoutMS = 30_000;
const maxMCPTimeoutMS = 300_000;
const bridgeGraceMS = 10_000;

export function mcpRefreshRequestTimeout(timeoutMS: unknown): number {
  const configured = Number(timeoutMS);
  const normalized = Number.isFinite(configured) && configured > 0 ? configured : defaultMCPTimeoutMS;
  return Math.min(normalized, maxMCPTimeoutMS) + bridgeGraceMS;
}
