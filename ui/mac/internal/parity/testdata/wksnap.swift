// snap URL out.png W H [light|dark] [js]: a key-looking WKWebView snapshot at 2x.
import AppKit
import WebKit
let a = CommandLine.arguments
let url = URL(string: a[1])!, out = a[2]
let w = Double(a[3])!, h = Double(a[4])!
let dark = a.count > 5 && a[5] == "dark"
let js = a.count > 6 ? a[6] : ""
let app = NSApplication.shared
app.setActivationPolicy(.prohibited)
class KeyWindow: NSWindow {
  override var isKeyWindow: Bool { true }
  override var canBecomeKey: Bool { true }
}
let win = KeyWindow(contentRect: NSRect(x: 0, y: 0, width: w, height: h), styleMask: [.borderless], backing: .buffered, defer: false)
win.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
let wv = WKWebView(frame: NSRect(x: 0, y: 0, width: w, height: h), configuration: WKWebViewConfiguration())
win.contentView = wv
let occSel = NSSelectorFromString("_setWindowOcclusionDetectionEnabled:")
if wv.responds(to: occSel) { typealias F = @convention(c) (AnyObject, Selector, Bool) -> Void; unsafeBitCast(wv.method(for: occSel), to: F.self)(wv, occSel, false) }
win.orderBack(nil)
wv.load(URLRequest(url: url))
DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
  wv.evaluateJavaScript(js.isEmpty ? "1" : js) { r, e in
    print("js:", r ?? "", e ?? "")
    DispatchQueue.main.asyncAfter(deadline: .now() + 0.7) {
      let c = WKSnapshotConfiguration(); c.rect = NSRect(x: 0, y: 0, width: w, height: h); c.snapshotWidth = NSNumber(value: w)
      wv.takeSnapshot(with: c) { img, err in
        guard let img = img, let cg = img.cgImage(forProposedRect: nil, context: nil, hints: nil) else { print("error", err as Any); exit(1) }
        let rep = NSBitmapImageRep(cgImage: cg)
        try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
        print("wrote", out, rep.pixelsWide, rep.pixelsHigh); exit(0)
      }
    }
  }
}
app.run()
