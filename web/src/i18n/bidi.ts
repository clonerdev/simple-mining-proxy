// Bidirectional-text helpers for right-to-left languages. Kept free of DOM
// access so that it can be unit-tested.

const LRI = '\u2066' // left-to-right isolate
const PDI = '\u2069' // pop directional isolate

// A run of Latin letters, digits and the punctuation that belongs to them,
// with single spaces between words: "40.46 TH/s", "stratum+tls://", ".env",
// "/status", "10/15 22:16:11", "TCP 6".
const technicalRun = /[./]?[A-Za-z0-9][A-Za-z0-9_.:/+@#%<>=-]*(?: [A-Za-z0-9][A-Za-z0-9_.:/+@#%<>=-]*)*/g

/**
 * In right-to-left text the bidi algorithm puts the unit of "48.38 TH/s"
 * before the number and moves the dot of ".env" to its other end. Every such
 * run is wrapped in a left-to-right isolate so technical values read exactly
 * as they do in English and in the Docker logs. Plain numbers are left alone,
 * and the punctuation that ends a sentence stays outside the isolate.
 */
export function isolateTechnicalRuns(text: string): string {
  // A value that went through a translation of its own ("<HOST>" inside a
  // longer sentence) arrives wrapped already; start from the plain text.
  return text.replaceAll(LRI, '').replaceAll(PDI, '').replace(technicalRun, (run) => {
    const [, body = run, tail = ''] = /^(.*?)([.:]*)$/s.exec(run) ?? []
    if (/^\d+$/.test(body)) return run
    return LRI + body + PDI + tail
  })
}
