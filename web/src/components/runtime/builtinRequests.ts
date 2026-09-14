// 每个节点只保留一个有效读取；写入令旧读取失效，即使底层未响应取消也不能回写旧状态。
export class BuiltinRequests<T, U> {
  private active = true;
  private visible = true;
  private generation = 0;
  private reader?: AbortController;
  private writer?: AbortController;
  private timer?: ReturnType<typeof setTimeout>;
  constructor(
    private read: (signal: AbortSignal) => Promise<T>,
    private write: (update: U, signal: AbortSignal) => Promise<T>,
    private publish: (snapshot: T) => void,
    private fail: (error: unknown) => void,
    private busy: (value: boolean) => void,
    private interval = 5000,
  ) {}
  private stopRead() {
    ++this.generation;
    clearTimeout(this.timer);
    this.reader?.abort();
    this.reader = undefined;
  }
  async refresh() {
    if (!this.active || !this.visible || this.writer || this.reader) return;
    clearTimeout(this.timer);
    const controller = this.reader = new AbortController();
    const ticket = ++this.generation;
    try {
      const snapshot = await this.read(controller.signal);
      if (this.active && ticket === this.generation) this.publish(snapshot);
    } catch (error) {
      if (this.active && ticket === this.generation) this.fail(error);
    } finally {
      if (this.reader === controller) this.reader = undefined;
      if (this.active && this.visible && ticket === this.generation && !this.writer) {
        this.timer = setTimeout(() => void this.refresh(), this.interval);
      }
    }
  }
  async update(update: U) {
    if (!this.active || this.writer) return;
    this.stopRead();
    const controller = this.writer = new AbortController();
    const ticket = this.generation;
    this.busy(true);
    try {
      const snapshot = await this.write(update, controller.signal);
      if (this.active && ticket === this.generation) this.publish(snapshot);
    } catch (error) {
      if (this.active) this.fail(error);
    } finally {
      this.writer = undefined;
      if (this.active) { this.busy(false); void this.refresh(); }
    }
  }
  setVisible(visible: boolean) {
    this.visible = visible;
    if (!visible) this.stopRead();
    else void this.refresh();
  }
  close() {
    this.active = false;
    this.stopRead();
    this.writer?.abort();
  }
}
