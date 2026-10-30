// Generated from aiworker packages/browser-profile; do not edit.
(() => {
// Canonical, self-contained function. Embedded unchanged by browserd and injected by Chrome.
async function profilePageState(mode, input) {
  const request = r => new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result)
    r.onerror = () => reject(r.error || new Error('IndexedDB request failed'))
    r.onblocked = () => reject(new Error('IndexedDB is in use; close other tabs for this site and retry'))
  })
  async function encode(root) {
    const nodes = [], seen = new Map()
    async function value(v) {
      if (v === null || typeof v === 'string' || typeof v === 'boolean') return v
      if (typeof v === 'number') return Number.isFinite(v) ? v : { type: 'number', value: String(v) }
      if (typeof v === 'undefined') return { type: 'undefined' }
      if (typeof v === 'bigint') return { type: 'bigint', value: String(v) }
      if (typeof v !== 'object') throw new Error('Unsupported IndexedDB value: ' + typeof v)
      if (seen.has(v)) return { ref: seen.get(v) }
      if (nodes.length >= 100000) throw new Error('IndexedDB graph exceeds 100000 nodes; export a smaller profile')
      const id = nodes.length; seen.set(v, id); nodes.push(null)
      let node
      if (v instanceof Date) node = { type: 'date', value: v.toISOString() }
      else if (v instanceof RegExp) node = { type: 'regexp', source: v.source, flags: v.flags }
      else if (v instanceof ArrayBuffer) node = { type: 'buffer', value: Array.from(new Uint8Array(v)) }
      else if (ArrayBuffer.isView(v)) node = { type: 'view', name: v.constructor.name, buffer: await value(v.buffer), offset: v.byteOffset, length: v.byteLength }
      else if (v instanceof Blob) node = { type: v instanceof File ? 'file' : 'blob', mime: v.type, name: v.name, lastModified: v.lastModified, value: Array.from(new Uint8Array(await v.arrayBuffer())) }
      else if (Array.isArray(v)) node = { type: 'array', length: v.length, entries: await Promise.all(Object.keys(v).map(async k => [k, await value(v[k])])) }
      else if (v instanceof Map) node = { type: 'map', entries: await Promise.all([...v].map(async ([k, x]) => [await value(k), await value(x)])) }
      else if (v instanceof Set) node = { type: 'set', entries: await Promise.all([...v].map(value)) }
      else if (Object.getPrototypeOf(v) === Object.prototype || Object.getPrototypeOf(v) === null) node = { type: 'object', entries: await Promise.all(Object.keys(v).map(async k => [k, await value(v[k])])) }
      else throw new Error('Unsupported IndexedDB object: ' + Object.prototype.toString.call(v))
      nodes[id] = node
      return { ref: id }
    }
    const rootValue = await value(root)
    return { root: rootValue, nodes }
  }
  function decode(graph) {
    if (!graph || !Array.isArray(graph.nodes) || graph.nodes.length > 100000) throw new Error('Invalid IndexedDB value graph')
    const cache = new Map()
    function value(v) {
      if (v === null || typeof v !== 'object') return v
      if ('ref' in v) {
        if (!Number.isInteger(v.ref) || v.ref < 0 || v.ref >= graph.nodes.length) throw new Error('Invalid value reference')
        if (cache.has(v.ref)) return cache.get(v.ref)
        const n = graph.nodes[v.ref]; let out
        switch (n.type) {
          case 'array': out = new Array(n.length); break
          case 'object': out = {}; break
          case 'map': out = new Map(); break
          case 'set': out = new Set(); break
          case 'date': out = new Date(n.value); break
          case 'regexp': out = new RegExp(n.source, n.flags); break
          case 'buffer': out = Uint8Array.from(n.value).buffer; break
          case 'blob': out = new Blob([Uint8Array.from(n.value)], { type: n.mime }); break
          case 'file': out = new File([Uint8Array.from(n.value)], n.name, { type: n.mime, lastModified: n.lastModified }); break
          case 'view': {
            const types = { Int8Array, Uint8Array, Uint8ClampedArray, Int16Array, Uint16Array, Int32Array, Uint32Array, Float32Array, Float64Array, BigInt64Array, BigUint64Array, DataView }
            const ctor = types[n.name]
            if (!ctor) throw new Error('Unsupported binary view: ' + n.name)
            const buffer = value(n.buffer)
            out = n.name === 'DataView' ? new DataView(buffer, n.offset, n.length) : new ctor(buffer, n.offset, n.length / ctor.BYTES_PER_ELEMENT)
            break
          }
          default: throw new Error('Unsupported value node: ' + n.type)
        }
        cache.set(v.ref, out)
        if (n.type === 'map') for (const [k, x] of n.entries) out.set(value(k), value(x))
        else if (n.type === 'set') for (const x of n.entries) out.add(value(x))
        else if (n.type === 'array' || n.type === 'object') for (const [k, x] of n.entries) Object.defineProperty(out, k, { value: value(x), writable: true, enumerable: true, configurable: true })
        return out
      }
      if (v.type === 'undefined') return undefined
      if (v.type === 'number' && ['NaN', 'Infinity', '-Infinity'].includes(v.value)) return Number(v.value)
      if (v.type === 'bigint') return BigInt(v.value)
      throw new Error('Unknown value encoding')
    }
    return value(graph.root)
  }
  // Codec modes also allow contract testing without browser storage.
  if (mode === 'encode') return encode(input)
  if (mode === 'decode') return decode(input)
  if (mode === 'export') {
    const databases = []
    for (const info of await indexedDB.databases()) {
      if (!info.name) throw new Error('Cannot export an unnamed IndexedDB database')
      const db = await request(indexedDB.open(info.name))
      try {
        const stores = []
        for (const name of db.objectStoreNames) {
          const store = db.transaction(name, 'readonly').objectStore(name)
          const keysRequest = request(store.getAllKeys()), valuesRequest = request(store.getAll())
          const indexes = [...store.indexNames].map(n => {
            const i = store.index(n)
            return { name: i.name, keyPath: i.keyPath, unique: i.unique, multiEntry: i.multiEntry }
          })
          const [keys, values] = await Promise.all([keysRequest, valuesRequest])
          let nextKey
          if (store.autoIncrement) {
            const probe = db.transaction(name, 'readwrite')
            const aborted = new Promise((resolve, reject) => { probe.onabort = resolve; probe.onerror = () => reject(probe.error) })
            nextKey = await request(probe.objectStore(name).add(store.keyPath === null ? null : {}))
            probe.abort() // Aborting rolls back both the probe row and the key generator.
            await aborted
          }
          stores.push({ name, keyPath: store.keyPath, autoIncrement: store.autoIncrement, ...(nextKey !== undefined ? { nextKey } : {}), indexes, records: await encode(keys.map((key, i) => ({ key, value: values[i] }))) })
        }
        databases.push({ name: db.name, version: db.version, stores })
      } finally { db.close() }
    }
    return { origin: location.origin, localStorage: Object.fromEntries(Object.keys(localStorage).map(k => [k, localStorage.getItem(k)])), indexedDB: databases }
  }
  if (mode !== 'import' || input.origin !== location.origin) throw new Error('Profile origin does not match this page')
  // Decode every value before modifying any storage at this origin.
  const decoded = input.indexedDB.map(db => ({ ...db, stores: db.stores.map(s => ({ ...s, records: decode(s.records) })) }))
  for (const db of decoded) for (const store of db.stores) if (!Array.isArray(store.records) || store.records.length > 100000 || Object.keys(store.records).length !== store.records.length || store.records.some(r => !r || !('key' in r) || !('value' in r))) throw new Error('Invalid IndexedDB records')
  for (const db of await indexedDB.databases()) await request(indexedDB.deleteDatabase(db.name))
  for (const schema of decoded) {
    const open = indexedDB.open(schema.name, schema.version)
    open.onupgradeneeded = () => {
      for (const s of schema.stores) {
        const store = open.result.createObjectStore(s.name, { keyPath: s.keyPath, autoIncrement: s.autoIncrement })
        for (const i of s.indexes) store.createIndex(i.name, i.keyPath, { unique: i.unique, multiEntry: i.multiEntry })
      }
    }
    const db = await request(open)
    try {
      if (schema.stores.length) await new Promise((resolve, reject) => {
        const tx = db.transaction(schema.stores.map(s => s.name), 'readwrite')
        tx.oncomplete = resolve; tx.onerror = () => reject(tx.error); tx.onabort = () => reject(tx.error || new Error('IndexedDB import aborted'))
        try {
          for (const s of schema.stores) {
            const store = tx.objectStore(s.name)
            for (const record of s.records) {
              if (s.keyPath === null) store.put(record.value, record.key)
              else store.put(record.value)
            }
            if (s.autoIncrement && s.nextKey > 1 && !s.records.some(r => r.key === s.nextKey - 1)) {
              const key = s.nextKey - 1
              if (s.keyPath === null) store.add(null, key)
              else {
                const sentinel = {}; let cursor = sentinel; const parts = s.keyPath.split('.')
                for (const part of parts.slice(0, -1)) { Object.defineProperty(cursor, part, { value: {}, enumerable: true }); cursor = cursor[part] }
                Object.defineProperty(cursor, parts.at(-1), { value: key, enumerable: true })
                store.add(sentinel)
              }
              store.delete(key)
            }
          }
        } catch (err) { tx.abort(); reject(err) }
      })
    } finally { db.close() }
  }
  localStorage.clear()
  for (const [k, v] of Object.entries(input.localStorage)) localStorage.setItem(k, v)
  return { origin: location.origin, databases: decoded.length, localStorageKeys: Object.keys(input.localStorage).length }
}

const PROFILE_FORMAT = 'syngy-browser-profile'
const PROFILE_VERSION = 1
const MAX_PROFILE_BYTES = 32 * 1024 * 1024
function canonicalOrigin(raw) {
  const u = new URL(raw)
  if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || raw !== u.origin) throw new Error('Use an exact HTTP(S) origin, for example https://example.com')
  return u.origin
}
function validateProfile(value) {
  if (!value || value.format !== PROFILE_FORMAT || value.version !== PROFILE_VERSION) throw new Error('Unsupported profile format/version; a syngy-browser-profile v1 package')
  if (new TextEncoder().encode(JSON.stringify(value)).length > MAX_PROFILE_BYTES) throw new Error('Profile exceeds the 32 MiB limit; fewer sites')
  if (!value.fingerprint || typeof value.fingerprint.seed !== 'string' || !value.fingerprint.config || typeof value.fingerprint.config !== 'object' || Array.isArray(value.fingerprint.config)) throw new Error('Profile requires fingerprint seed and config')
  const fp = value.fingerprint.config
  if (!value.fingerprint.seed || value.fingerprint.seed.length > 128 || fp.seed !== value.fingerprint.seed || ['locale','timezone','os','platform','userAgent','acceptLanguage','webglVendor','webglRenderer'].some(k => typeof fp[k] !== 'string' || !fp[k]) || !Array.isArray(fp.languages) || !fp.languages.length || fp.languages.some(l => typeof l !== 'string' || !l)) throw new Error('Incomplete fingerprint configuration; again with the Chrome plugin or cloud browser')
  for (const key of ['viewportWidth','viewportHeight','screenWidth','screenHeight','deviceScaleFactor','hardwareConcurrency','deviceMemory']) if (!Number.isFinite(fp[key]) || fp[key] <= 0) throw new Error('Invalid fingerprint field: ' + key)
  try { new Intl.DateTimeFormat('en', { timeZone: fp.timezone }) } catch { throw new Error('Invalid fingerprint timezone') }
  if (!Array.isArray(value.origins) || !value.origins.length || value.origins.length > 50 || !Array.isArray(value.cookies) || value.cookies.length > 10000) throw new Error('Profile requires 1–50 origins and at most 10000 cookies')
  const origins = new Set()
  for (const site of value.origins) {
    canonicalOrigin(site.origin)
    if (origins.has(site.origin)) throw new Error('Duplicate origin in profile')
    origins.add(site.origin)
    if (!site.localStorage || Array.isArray(site.localStorage) || typeof site.localStorage !== 'object' || Object.values(site.localStorage).some(v => typeof v !== 'string') || !Array.isArray(site.indexedDB)) throw new Error('Invalid origin storage')
    const dbNames = new Set()
    for (const db of site.indexedDB) {
      if (typeof db.name !== 'string' || !db.name || dbNames.has(db.name) || !Number.isSafeInteger(db.version) || db.version <= 0 || !Array.isArray(db.stores)) throw new Error('Invalid IndexedDB schema')
      dbNames.add(db.name)
      const storeNames = new Set()
      for (const store of db.stores) {
        if (typeof store.name !== 'string' || storeNames.has(store.name) || typeof store.autoIncrement !== 'boolean' || !Array.isArray(store.indexes) || !store.records || !Array.isArray(store.records.nodes)) throw new Error('Invalid IndexedDB store')
        storeNames.add(store.name)
        if (store.autoIncrement && (!Number.isSafeInteger(store.nextKey) || store.nextKey < 1)) throw new Error('Missing IndexedDB auto-increment counter; the profile again')
        const keyPath = k => k === null || typeof k === 'string' || (Array.isArray(k) && k.every(x => typeof x === 'string'))
        if (!keyPath(store.keyPath)) throw new Error('Invalid IndexedDB key path')
        const indexes = new Set()
        for (const index of store.indexes) {
          if (typeof index.name !== 'string' || indexes.has(index.name) || !keyPath(index.keyPath) || index.keyPath === null || typeof index.unique !== 'boolean' || typeof index.multiEntry !== 'boolean') throw new Error('Invalid IndexedDB index')
          indexes.add(index.name)
        }
      }
    }
  }
  const identities = new Set()
  for (const c of value.cookies) {
    if (typeof c.name !== 'string' || !c.name || typeof c.value !== 'string' || typeof c.domain !== 'string' || !c.domain || typeof c.path !== 'string' || !c.path.startsWith('/') || typeof c.secure !== 'boolean' || typeof c.httpOnly !== 'boolean' || typeof c.hostOnly !== 'boolean' || !['unspecified', 'no_restriction', 'lax', 'strict'].includes(c.sameSite)) throw new Error('Invalid Cookie scope or attributes')
    if (c.expirationDate !== undefined && (!Number.isFinite(c.expirationDate) || c.expirationDate <= 0)) throw new Error('Invalid Cookie expiry')
    if (c.partitionKey !== undefined && (!c.partitionKey || typeof c.partitionKey.topLevelSite !== 'string' || (c.partitionKey.hasCrossSiteAncestor !== undefined && typeof c.partitionKey.hasCrossSiteAncestor !== 'boolean'))) throw new Error('Invalid partitioned Cookie key')
    const domain = c.domain.replace(/^\./, '').toLowerCase()
    if (![...origins].some(o => { const h = new URL(o).hostname; return h === domain || (!c.hostOnly && h.endsWith('.' + domain)) })) throw new Error('Cookie is outside the selected profile origins')
    if (c.hostOnly && c.domain.startsWith('.')) throw new Error('Host-only Cookie cannot use a domain scope')
    const id = JSON.stringify([c.name, c.domain, c.path, c.partitionKey ? [c.partitionKey.topLevelSite, c.partitionKey.hasCrossSiteAncestor ?? false] : null])
    if (identities.has(id)) throw new Error('Duplicate Cookie scope')
    identities.add(id)
  }
  return value
}

return { profilePageState, validateProfile };
})()