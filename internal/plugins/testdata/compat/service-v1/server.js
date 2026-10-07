function onEvent(ev, ctx) { ctx.data.increment('events'); ctx.data.set('last', ev.name) }
function onSchedule(ctx) { ctx.log('tick') }
function digest(req, ctx) { var v = ctx.panel.view(); return { sessions: v.sessions.length, projects: v.projects.length, caller: req.caller, caps: ctx.caps } }
function echo(req, ctx) { return { body: req.body, q: req.query } }
function onInbound(req, ctx) { ctx.data.set('last', 'inbound'); return { ok: true } }
