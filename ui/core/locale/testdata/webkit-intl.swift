// Runs webkit-intl.js in a WKWebView and writes what it returns, with the macOS version, to
// a file: swiftc webkit-intl.swift -o webkit-intl && ./webkit-intl webkit-intl.js webkit-intl-macos.json
import AppKit
import WebKit

let js = try! String(contentsOfFile: CommandLine.arguments[1], encoding: .utf8)
let outPath = CommandLine.arguments[2]

class D: NSObject, WKNavigationDelegate {
  func webView(_ w: WKWebView, didFinish n: WKNavigation!) {
    w.evaluateJavaScript(js) { r, e in
      if let s = r as? String {
        let v = ProcessInfo.processInfo.operatingSystemVersion
        let json = "{\"macos\":\"\(v.majorVersion).\(v.minorVersion)\"," + s.dropFirst()
        try! json.write(toFile: outPath, atomically: true, encoding: .utf8)
      } else {
        print("error", e as Any)
      }
      exit(0)
    }
  }
}

let app = NSApplication.shared
let w = WKWebView(frame: NSRect(x: 0, y: 0, width: 100, height: 100))
let d = D()
w.navigationDelegate = d
w.loadHTMLString("<html><body>x</body></html>", baseURL: nil)
app.run()
