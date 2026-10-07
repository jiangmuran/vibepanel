function click(req, ctx) {
  ctx.data.increment('clicks')
  return { clicks: ctx.data.get('clicks'), greeting: ctx.settings.greeting }
}
