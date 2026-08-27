import type { ShareDashboard, ShareWidget } from '../../protocol/wire'
import { Gauge, Machine, Unknown, Uptime } from './machine'
import { Attention, BigNumber, Caption, Clock, Output, States } from './numbers'
import { CPUTop, Exits, Projects, SessionGrid, SessionList, Todos } from './sessions'
import { SpendBars, SpendCompare, SpendHeatmap, SpendRate, SpendSplit, SpendTotals } from './spend'

/**
 * One widget, chosen by kind.
 *
 * A switch over a string, and the default branch is the security-relevant half:
 * a board is data that arrived from a database, so `kind` is a value from
 * outside this file. Rendering an unknown one as an empty tile — rather than
 * falling back to some "sensible" widget, or indexing a map with it, or
 * building a component name from it — is what keeps a stored board from
 * choosing a code path. The server refuses unknown kinds on the way in and
 * drops them on the way out; this is the third place the same rule is applied,
 * because it is the one that runs on somebody else's machine.
 *
 * No widget receives anything but the response and the clock. There is no
 * fetch, no URL, no `dangerouslySetInnerHTML` and no dynamic import below this
 * line, and a board has no vocabulary that could ask for one.
 */
export function Widget({
  w,
  data,
  now,
}: {
  w: ShareWidget
  data: ShareDashboard
  now: number
}) {
  switch (w.kind) {
    case 'attention':
      return <Attention w={w} data={data} now={now} />
    case 'states':
      return <States w={w} data={data} />
    case 'bignumber':
      return <BigNumber w={w} data={data} now={now} />
    case 'clock':
      return <Clock w={w} />
    case 'caption':
      return <Caption w={w} />
    case 'sessiongrid':
      return <SessionGrid w={w} data={data} now={now} />
    case 'sessionlist':
      return <SessionList w={w} data={data} now={now} />
    case 'projects':
      return <Projects w={w} data={data} />
    case 'todos':
      return <Todos w={w} data={data} />
    case 'output':
      return <Output w={w} data={data} />
    case 'machine':
      return <Machine w={w} data={data} />
    case 'gauge':
      return <Gauge w={w} data={data} />
    case 'uptime':
      return <Uptime w={w} data={data} />
    case 'cputop':
      return <CPUTop w={w} data={data} />
    case 'exits':
      return <Exits w={w} data={data} />
    case 'spendtotals':
      return <SpendTotals w={w} data={data} />
    case 'spendrate':
      return <SpendRate w={w} data={data} />
    case 'spendcompare':
      return <SpendCompare w={w} data={data} />
    case 'spendbars':
      return <SpendBars w={w} data={data} />
    case 'spendsplit':
      return <SpendSplit w={w} data={data} />
    case 'spendheatmap':
      return <SpendHeatmap w={w} data={data} />
    default:
      return <Unknown w={w} />
  }
}
