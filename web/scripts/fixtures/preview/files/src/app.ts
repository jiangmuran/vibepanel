import { readFileSync } from 'node:fs'

/** A doc comment. */
export interface Thing {
  id: string
  count: number
}

const PATTERN = /^[a-z]+\d*$/i
const template = `value: ${1 + 2}`

export class Store<T extends Thing> {
  private items = new Map<string, T>()

  add(item: T): void {
    if (!PATTERN.test(item.id)) throw new Error(`bad id ${item.id}`)
    this.items.set(item.id, item)
  }
}

export default readFileSync
