import { useEffect, useMemo, useRef, useState } from 'react'

const fallbackTimeZones = [
  'Africa/Cairo', 'Africa/Johannesburg', 'America/Anchorage', 'America/Chicago', 'America/Denver',
  'America/Los_Angeles', 'America/Mexico_City', 'America/New_York', 'America/Phoenix', 'America/Sao_Paulo',
  'Asia/Bangkok', 'Asia/Dubai', 'Asia/Hong_Kong', 'Asia/Kolkata', 'Asia/Seoul', 'Asia/Shanghai',
  'Asia/Singapore', 'Asia/Tokyo', 'Australia/Perth', 'Australia/Sydney', 'Europe/Amsterdam', 'Europe/Berlin',
  'Europe/London', 'Europe/Moscow', 'Europe/Paris', 'Pacific/Auckland', 'Pacific/Honolulu',
]

function supportedTimeZones() {
  let zones = fallbackTimeZones
  try {
    if (typeof Intl.supportedValuesOf === 'function') zones = Intl.supportedValuesOf('timeZone')
  } catch {}
  return [...new Set(['UTC', ...zones])].sort((left, right) => {
    if (left === 'UTC') return -1
    if (right === 'UTC') return 1
    return left.localeCompare(right)
  })
}

function zoneOffset(zone) {
  try {
    return new Intl.DateTimeFormat('en', { timeZone: zone, timeZoneName: 'shortOffset' })
      .formatToParts(new Date())
      .find(part => part.type === 'timeZoneName')?.value || ''
  } catch {
    return ''
  }
}

const timeZoneOptions = supportedTimeZones().map(zone => ({ zone, offset: zoneOffset(zone) }))

export default function TimezoneSelect({ value, onChange, disabled = false }) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [activeIndex, setActiveIndex] = useState(0)
  const containerRef = useRef(null)
  const searchRef = useRef(null)
  const options = useMemo(() => {
    let available = timeZoneOptions
    if (value && !timeZoneOptions.some(item => item.zone === value)) available = [{ zone: value, offset: zoneOffset(value) }, ...timeZoneOptions]
    const term = search.trim().toLowerCase()
    return available.filter(item => !term || `${item.zone} ${item.offset}`.toLowerCase().includes(term))
  }, [search, value])

  useEffect(() => {
    if (!open) return undefined
    searchRef.current?.focus()
    const closeOnOutsideClick = event => {
      if (!containerRef.current?.contains(event.target)) setOpen(false)
    }
    document.addEventListener('pointerdown', closeOnOutsideClick)
    return () => document.removeEventListener('pointerdown', closeOnOutsideClick)
  }, [open])

  function choose(zone) {
    onChange(zone)
    setOpen(false)
    setSearch('')
    setActiveIndex(0)
  }

  function handleSearchKeyDown(event) {
    if (event.key === 'Escape') {
      event.preventDefault()
      setOpen(false)
      setSearch('')
    } else if (event.key === 'ArrowDown' && options.length) {
      event.preventDefault()
      setActiveIndex(index => (index + 1) % options.length)
    } else if (event.key === 'ArrowUp' && options.length) {
      event.preventDefault()
      setActiveIndex(index => (index - 1 + options.length) % options.length)
    } else if (event.key === 'Enter' && options.length) {
      event.preventDefault()
      choose(options[activeIndex]?.zone || options[0].zone)
    }
  }

  return <div className="timezone-picker" ref={containerRef}>
    <button
      type="button"
      className="timezone-trigger"
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-label={`系统时区，当前 ${value || 'UTC'}`}
      disabled={disabled}
      onClick={() => setOpen(current => !current)}
    >
      <span>{value || 'UTC'}</span><span className="timezone-trigger-icon" aria-hidden="true">⌄</span>
    </button>
    {open && <div className="timezone-dropdown">
      <input
        ref={searchRef}
        type="search"
        role="searchbox"
        aria-label="搜索 IANA 时区"
        placeholder="搜索城市或时区，例如 Shanghai"
        value={search}
        onChange={event => { setSearch(event.target.value); setActiveIndex(0) }}
        onKeyDown={handleSearchKeyDown}
        autoComplete="off"
        spellCheck="false"
      />
      <div className="timezone-options" role="listbox" aria-label="可用时区">
        {options.map(({ zone, offset }, index) => <button
          type="button"
          role="option"
          aria-selected={zone === value}
          className={index === activeIndex ? 'timezone-option active' : 'timezone-option'}
          key={zone}
          onMouseEnter={() => setActiveIndex(index)}
          onClick={() => choose(zone)}
        ><span>{zone}</span><small>{offset}</small></button>)}
        {!options.length && <p className="timezone-no-results">没有匹配的时区</p>}
      </div>
      <p className="timezone-search-help">只能选择列表中的有效时区，搜索内容不会作为设置保存。</p>
    </div>}
  </div>
}
