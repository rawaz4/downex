// The cases of ui/core/locale's TestMatchesWebKit, run in a WKWebView by webkit-intl.swift.
(function(){
const out = {meta:{}, dates:[], numbers:[], names:[]};
out.meta.language = navigator.language;
out.meta.intlDefault = new Intl.DateTimeFormat().resolvedOptions().locale; // = navigator.language
const styles = {
 time:{ hour: '2-digit', minute: '2-digit', hourCycle: 'h23' },
 clock:{ hour: 'numeric', minute: '2-digit' },
 monthDay:{ month: 'short', day: 'numeric' },
 date:{ year: 'numeric', month: 'short', day: 'numeric' },
 ymd:{ year: 'numeric', month: '2-digit', day: '2-digit' },
 full:{ year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' },
};
const locales = ['en-US','en-GB','en-IQ','de-DE','fr-FR','ar','ar-IQ','fa-IR','ja-JP-u-ca-japanese','zh-TW','xx-YY','en-US-u-hc-h12','hi-IN','he-IL','th-TH-u-ca-buddhist','de-u-nu-arab'];
const zones = ['UTC', 'Asia/Baghdad', 'America/New_York', 'Australia/Lord_Howe', 'Mars/Olympus', 'asia/baghdad'];
const instants = [1, 1700000004000, 1792508400000, 1792415100000, 1761670770000, 1761692700000, 4102444799000, -62198755200000, -62135596800000, -12219292800000, -12219292800001, -8.64e15, 8.64e15, 946684800000, 1735689599999, 1.9, -1.9];
out.instants = instants;
for (const tag of locales) {
  for (const zone of zones) {
    for (const [name, opts] of Object.entries(styles)) {
      let df;
      try { df = new Intl.DateTimeFormat(tag, zone ? {...opts, timeZone: zone} : opts); }
      catch (e) { out.dates.push({tag, zone, style:name, error:String(e)}); continue; }
      const outs = instants.map(ms => { try { return df.format(ms); } catch(e) { return null; } });
      const rec = {tag, zone, style:name, outs, resolved: df.resolvedOptions().locale};
      if (name === 'ymd') rec.years = instants.map(ms => { try { return (df.formatToParts(ms).find(p=>p.type==='year')||{}).value ?? null; } catch(e) { return null; } });
      out.dates.push(rec);
    }
  }
  const nums = [0, 1, -1, 0.5, 1.5, 2.5, 12.5, 999.5, 1234567.891, -42.25, 1e21, 1e-7, 0.05, 0.15, 1.005, 99.95, 1.05, -0, NaN, Infinity, -Infinity, 123456789012345680000, 0.045, 3.14159, 1000, 10000, 100000, 482000000, 1.5e300];
  for (const d of [0,1,2,3,20]) {
    try {
      const nf = new Intl.NumberFormat(tag, {minimumFractionDigits:d, maximumFractionDigits:d});
      out.numbers.push({tag, digits:d, ins: nums.map(n => Object.is(n,-0)?'-0':String(n)), outs: nums.map(n=>nf.format(n)), resolved: nf.resolvedOptions().locale, nu: nf.resolvedOptions().numberingSystem});
    } catch(e) { out.numbers.push({tag, digits:d, error:String(e)}); }
  }
}
const names = ['en','en-US','en-GB','de','de-DE','fr','pt-BR','pt-PT','zh','zh-Hans','zh-Hant','zh-Hans-CN','zh-TW','es-419','sr-Latn','ar','ar-IQ','ar-001','ku','ckb','fa','ja','ko','hi','he','iw','in','id','und','xx','xx-YY','qaa','mul','zxx','yue','nb','no','fil','tl','sh','mo','root','en-Latn','en-Latn-US','de-CH','de-AT','fr-CA','en-AU','nl-BE','es-MX','es-ES','ast','gsw','sgn','sw','orig','EN','En-gb','en_US','en-u-nu-latn','x-foo','e','toolonglanguage','abcde','de-1996','sl-rozaj','zh-min-nan','i-klingon','cmn','arb','pes','sv-FI','az-Cyrl','az-Latn','uz-Arab','pa-Arab','sd','ms-Arab','be-tarask','ca-valencia','en-001','en-150','es-US','fr-CH','it-CH','ro-MD','sr-ME','zh-HK','zh-MO','zh-SG','ru-UA','tok','vec','yi','ji','jw','aar','eng','deu','ger','und-Latn','und-US','ar-Arab','en-GB-oed','zh-cmn-Hans','hy-arevela','de-Latn-DE-1996','es-Latn','fr-FR-u-ca-gregory'];
const dn = new Intl.DisplayNames(['en'], {type:'language', fallback:'code'});
for (const t of names) { let v; try { v = dn.of(t); } catch(e) { v = {throws:String(e)}; } out.names.push({tag:t, out:v}); }
return JSON.stringify(out);
})()
