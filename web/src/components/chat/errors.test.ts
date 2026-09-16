import { describe, expect, it } from 'vitest'

import { setLang } from '../../i18n'
import { chatError } from './errors'

describe('chatError', () => {
  it('says the setup refusals in the page language and leaves the rest', () => {
    setLang('zh')
    expect(chatError(new Error('chat: nobody is paired on telegram'))).toBe('telegram 上还没有人配对')
    expect(chatError(new Error('claude: approve: tmux has no key called "Entr"'))).toBe('tmux 没有叫 Entr 的键')
    expect(chatError(new Error('default: quiet hours "25:00-08:00" is not HH:MM-HH:MM'))).toBe('安静时段写成 23:00-08:00')
    expect(chatError(new Error('feishu: app_id is required'))).toBe('没填：app_id')
    expect(chatError(new Error('telegram: a bot token is required'))).toBe('没填：bot token')
    expect(chatError(new Error('something else'))).toBe('something else')
  })
})
