// On-device text and face detection with Apple Vision (macOS only).
//
// Usage: swift vision-detect.swift <image>   (detect.py compiles and caches it)
//
// Prints JSON to stdout: {"width", "height", "words": [...], "faces": [...]}.
// Every box is {"x", "y", "w", "h"} in pixels with the origin at the top left.
// Words carry "text", "line" (index of the OCR line) and "conf". Nothing is
// sent anywhere: Vision runs on the device.

import AppKit
import Foundation
import Vision

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(1)
}

guard CommandLine.arguments.count == 2 else { fail("usage: vision-detect <image>") }
let url = URL(fileURLWithPath: CommandLine.arguments[1])
guard let image = NSImage(contentsOf: url),
      let cg = image.cgImage(forProposedRect: nil, context: nil, hints: nil)
else { fail("cannot read image: \(url.path)") }
let W = Double(cg.width), H = Double(cg.height)

func box(_ r: CGRect) -> [String: Double] {
    // Vision uses normalized coordinates with the origin at the bottom left.
    return ["x": (r.minX * W).rounded(), "y": ((1 - r.maxY) * H).rounded(),
            "w": (r.width * W).rounded(), "h": (r.height * H).rounded()]
}

let text = VNRecognizeTextRequest()
text.recognitionLevel = .accurate
text.usesLanguageCorrection = false
text.automaticallyDetectsLanguage = true
let faces = VNDetectFaceRectanglesRequest()

do {
    try VNImageRequestHandler(cgImage: cg, options: [:]).perform([text, faces])
} catch { fail("vision failed: \(error)") }

var words: [[String: Any]] = []
for (lineIndex, obs) in (text.results ?? []).enumerated() {
    guard let cand = obs.topCandidates(1).first else { continue }
    let s = cand.string
    // Split the line on whitespace and ask Vision for each word's own box, so a
    // single name can be hidden without covering the rest of the line.
    var idx = s.startIndex
    while idx < s.endIndex {
        while idx < s.endIndex && s[idx].isWhitespace { idx = s.index(after: idx) }
        var end = idx
        while end < s.endIndex && !s[end].isWhitespace { end = s.index(after: end) }
        if idx < end {
            let r = (try? cand.boundingBox(for: idx..<end))?.boundingBox ?? obs.boundingBox
            var w: [String: Any] = box(r)
            w["text"] = String(s[idx..<end])
            w["line"] = lineIndex
            w["conf"] = Double(cand.confidence)
            words.append(w)
        }
        idx = end
    }
}

let faceBoxes = (faces.results ?? []).map { box($0.boundingBox) }
let out: [String: Any] = ["width": W, "height": H, "words": words, "faces": faceBoxes]
let data = try! JSONSerialization.data(withJSONObject: out, options: [.sortedKeys])
FileHandle.standardOutput.write(data)
