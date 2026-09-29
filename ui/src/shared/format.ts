

export function humanize(value: string) { return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase()) }

export function verb(value: string) { return value.includes('restart') ? 'restart' : value.includes('pause') ? 'pause' : value.includes('resume') ? 'resume' : 'action' }

export function safeJSON(value: unknown) { if (typeof value !== 'string') return value; try { return JSON.parse(value) } catch { return value } }

export function stringArray(value: unknown) { return Array.isArray(value) ? value.map(String).filter(Boolean) : [] }
