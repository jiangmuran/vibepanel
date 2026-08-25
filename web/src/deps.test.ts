import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

/**
 * The frontend's stated constraints, as a test rather than a paragraph.
 *
 * AGENTS.md rules out a component library and a state library: useState and
 * useReducer plus fetch and one WebSocket. That is a decision worth keeping on
 * purpose — every one of those libraries arrives for a good local reason and
 * none of them ever leaves — and nothing was enforcing it. The mirror of the
 * dependency guard on the Go side, which exists for the same reason.
 */
const pkg = JSON.parse(
  readFileSync(new URL('../package.json', import.meta.url), 'utf8'),
) as {
  scripts: Record<string, string>
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
}

const all = { ...pkg.dependencies, ...pkg.devDependencies }

describe('dependencies', () => {
  it('has no component library', () => {
    // lucide-react is icons, not components, and is named in AGENTS.md.
    for (const name of [
      '@mui/material',
      'antd',
      'react-bootstrap',
      '@chakra-ui/react',
      '@radix-ui/react-dialog',
      '@headlessui/react',
      'shadcn-ui',
    ]) {
      expect(all, `${name} is a component library; the panel builds its own`).not.toHaveProperty(
        name,
      )
    }
  })

  it('has no state library', () => {
    for (const name of ['redux', '@reduxjs/toolkit', 'zustand', 'jotai', 'mobx', 'recoil']) {
      expect(
        all,
        `${name} is a state library; the panel uses useState/useReducer and one socket`,
      ).not.toHaveProperty(name)
    }
  })

  it('runs vitest, not jest, and does not use prettier', () => {
    for (const name of ['jest', 'ts-jest', '@types/jest', 'prettier', 'eslint-config-prettier']) {
      expect(all, `${name} is ruled out by AGENTS.md`).not.toHaveProperty(name)
    }
    expect(all).toHaveProperty('vitest')
    expect(all).toHaveProperty('eslint')
  })

  it('keeps the pieces the terminal is built on', () => {
    // A dependency quietly swapped out is the same problem as one added, and
    // only the second is obvious.
    for (const name of ['@xterm/xterm', '@xterm/addon-fit', 'react', 'lucide-react']) {
      expect(all, `${name} is what the terminal and the interface are built on`).toHaveProperty(
        name,
      )
    }
  })
})

describe('scripts', () => {
  it('exposes every check that exists', () => {
    // A harness with no script is a harness nobody runs. scale-check spent a
    // while in exactly that state: written, committed, and unreachable through
    // npm because the entry was never added.
    for (const name of ['check:render', 'check:stress', 'check:restart', 'check:scale']) {
      expect(pkg.scripts, `${name} is missing, so that check has no way to be run`).toHaveProperty(
        name,
      )
    }
  })
})
