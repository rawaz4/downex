// wkplay URL OUT.png WIDTH HEIGHT light|dark STEPS.json
//
// The parity harness's reference: today's Mac window is the Svelte UI in a WKWebView (Wails'
// WebKit window). This loads the demo page in a WKWebView of WIDTH x HEIGHT points in a
// window of the given appearance (prefers-color-scheme follows it), waits for the list (or
// the progress window), plays STEPS (a JSON array of async JavaScript function bodies, each
// awaited, 400 ms apart; a string result is a failure), and writes a 2x snapshot. It prints
// "ok" or "fail: <why>". Animations run as in the real window: the snapshot is taken after
// they settle.
import AppKit
import WebKit

let a = CommandLine.arguments
guard a.count >= 7 else { print("fail: usage"); exit(2) }
let url = URL(string: a[1])!, out = a[2]
let w = Double(a[3])!, h = Double(a[4])!
let dark = a[5] == "dark"
let steps = (try? JSONSerialization.jsonObject(with: Data(contentsOf: URL(fileURLWithPath: a[6])))) as? [String] ?? []

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)
// A window that says it is key (it never takes the keyboard from the user's own windows):
// WebKit gives the page focus (:focus, caret, focus rings) only in a key window, as the real
// Wails window is while it is used.
class KeyWindow: NSWindow {
  override var isKeyWindow: Bool { true }
  override var canBecomeKey: Bool { true }
}
let win = KeyWindow(contentRect: NSRect(x: 0, y: 0, width: w, height: h), styleMask: [.borderless], backing: .buffered, defer: false)
win.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
let cfg = WKWebViewConfiguration()
let wv = WKWebView(frame: NSRect(x: 0, y: 0, width: w, height: h), configuration: cfg)
win.contentView = wv
let occSel = NSSelectorFromString("_setWindowOcclusionDetectionEnabled:")
if wv.responds(to: occSel) {
  typealias F = @convention(c) (AnyObject, Selector, Bool) -> Void
  unsafeBitCast(wv.method(for: occSel), to: F.self)(wv, occSel, false)
}
win.orderBack(nil)
win.makeFirstResponder(wv)
wv.load(URLRequest(url: url))

func run(_ body: String, _ done: @escaping (Any?) -> Void) {
  wv.callAsyncJavaScript(body, arguments: [:], in: nil, in: .page) { r in
    switch r {
    case .success(let v): done(v)
    case .failure(let e): done("error " + String(describing: e).prefix(200))
    }
  }
}

func after(_ s: Double, _ f: @escaping () -> Void) { DispatchQueue.main.asyncAfter(deadline: .now() + s, execute: f) }

func snapshot() {
  let c = WKSnapshotConfiguration()
  c.rect = NSRect(x: 0, y: 0, width: w, height: h)
  c.snapshotWidth = NSNumber(value: w)
  wv.takeSnapshot(with: c) { img, err in
    guard let img = img, let cg = img.cgImage(forProposedRect: nil, context: nil, hints: nil) else { print("fail: snapshot", err as Any); exit(1) }
    let rep = NSBitmapImageRep(cgImage: cg)
    try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
    print("ok")
    exit(0)
  }
}

func play(_ i: Int) {
  if i >= steps.count { after(1.2, snapshot); return }
  run(steps[i]) { v in
    if let s = v as? String { print("fail: step \(i): \(s)"); exit(1) }
    if let d = v as? [String: Any], let j = try? JSONSerialization.data(withJSONObject: d) {
      print("probe: " + String(data: j, encoding: .utf8)!)
    }
    after(0.4) { play(i + 1) }
  }
}

var tries = 0
func waitReady() {
  run("return !!document.querySelector('[role=row][data-id], .pw')") { v in
    if (v as? Bool) == true || (v as? NSNumber)?.boolValue == true { after(0.8) { play(0) }; return }
    tries += 1
    if tries > 100 { print("fail: page not ready"); exit(1) }
    after(0.1, waitReady)
  }
}
after(0.2, waitReady)
after(90) { print("fail: timeout"); exit(1) }
app.run()
