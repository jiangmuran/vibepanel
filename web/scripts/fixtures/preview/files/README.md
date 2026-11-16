# Fixture project

A paragraph with **bold**, *emphasis*, ~~struck~~, `code`, a [link](#tables)
and an autolink https://example.com.

## Tables

| left | centre | right |
|:-----|:------:|------:|
| a    |   b    |     c |
| 中文 |  ✓     |   1.5 |

## Lists

- one
  - nested
    - deeper
- two

1. first
2. second

- [x] done
- [ ] not yet

## Code

```ts
export function add(a: number, b: number): number {
  return a + b // a comment
}
```

## Images

![a pixel from the project](img/alpha.png)

![a remote image](https://example.com/remote.png)

## Raw HTML

<div onclick="alert(1)">raw html is shown as text</div>
<script>alert('no')</script>

## Footnotes

A claim.[^1]

[^1]: Its source.

> A quote, with a line
> that continues.
