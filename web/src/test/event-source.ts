export class TestEventSource extends EventTarget {
  static instances: TestEventSource[] = []
  readyState = 0
  constructor(readonly url: string) {
    super()
    TestEventSource.instances.push(this)
  }
  emit(type: string) { this.dispatchEvent(new MessageEvent(type, { data: '{}' })) }
  close() { this.readyState = 2 }
}
