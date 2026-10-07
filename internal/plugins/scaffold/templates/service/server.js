// Runs inside the panel, in a sandbox with a budget. ctx has one member per
// granted capability: ctx.panel.view() exists because read:panel is asked for.
function onEvent(ev, ctx) {
  ctx.data.increment('transitions')
  ctx.data.set('last', ev.name + ' ' + (ev.state || ''))
}

function onSchedule(ctx) {
  ctx.log('tick: ' + ctx.panel.view().sessions.length + ' sessions')
}

// GET …/x/summary from the pane (vp.route('GET', 'summary')) or the owner
// (/api/ext/__ID__/summary).
function summary(req, ctx) {
  var v = ctx.panel.view()
  return { sessions: v.sessions.length, waiting: v.sessions.filter(function (s) { return s.state === 'waiting' }).length,
           transitions: ctx.data.get('transitions'), caller: req.caller }
}
