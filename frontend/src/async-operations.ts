// UI request ownership is separate from transport authentication. A later history
// refresh supersedes an earlier one even when both use the same valid session.
export class AsyncOperations {
  private generation = 0;
  private nextId = 0;
  private current = new Map<string, number>();
  private blocking = new Set<number>();

  get busy(): boolean { return this.blocking.size > 0; }

  reset(): void {
    this.generation++;
    this.current.clear();
    this.blocking.clear();
  }

  begin(name: string, blocking = true) {
    const generation = this.generation;
    const id = ++this.nextId;
    const previous = this.current.get(name);
    if (previous !== undefined) this.blocking.delete(previous);
    this.current.set(name, id);
    if (blocking) this.blocking.add(id);
    const isCurrent = () => generation === this.generation && this.current.get(name) === id;
    return {
      isCurrent,
      finish: () => {
        const owned = isCurrent();
        if (owned) this.current.delete(name);
        this.blocking.delete(id);
        return owned;
      },
    };
  }
}
