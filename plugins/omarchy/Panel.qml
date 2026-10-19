import QtQuick
import Quickshell
import qs.Commons
import qs.Ui

// Nested detail panel for dev.fitr.evidence. Checked 2026-10-09 against the
// Quattro bar-widget tutorial: Panel manageIpc false, KeyboardPanel, and
// PanelKeyCatcher. Opening the panel refreshes sealed status. It does not
// start a measurement. The run control is a second click and calls back into
// the bar widget, which is the only place that builds a fitr command.
Panel {
  id: root
  moduleName: "dev.fitr.evidence"
  manageIpc: false

  property var anchorItem: null
  property var hostWidget: null
  property bool requestMeasurement: false

  function open() {
    root.requestMeasurement = false
    if (root.hostWidget && root.hostWidget.refreshStatus) root.hostWidget.refreshStatus()
    root.controller.show()
  }

  function close() {
    root.controller.hide()
  }

  function toggle() {
    if (root.opened) root.close()
    else root.open()
  }

  function closeForPopoutSwitch() {
    root.close()
  }

  function switchPanel(direction) {
    if (root.bar && typeof root.bar.switchPanelFrom === "function")
      return root.bar.switchPanelFrom(root.hostWidget || root, direction)
    return false
  }

  function urgent(state) {
    switch (String(state || "")) {
    case "stale":
    case "unavailable":
    case "unsupported":
    case "unresolved":
    case "disproven":
    case "blocked":
    case "observed_exceeded":
      return true
    default:
      return false
    }
  }

  // quattro Bar.qml exposes bar.urgent as Commons.Color.bar.active.
  // Checked 2026-10-18 in omacom/omarchy shell/plugins/bar/Bar.qml.
  // A missing property keeps the bar foreground. Bold plus the state word
  // still mark the row when that colour is ignored.
  function urgentColor() {
    if (root.bar && root.bar.urgent !== undefined)
      return root.bar.urgent
    return root.barForeground
  }

  function measurementReady() {
    var doc = root.hostWidget ? root.hostWidget.document : null
    return doc && doc.next && doc.next.effect === "explicit-local" && doc.next.local_proven === true
  }

  function runRequested() {
    if (!root.requestMeasurement) {
      root.requestMeasurement = true
      return
    }
    if (root.hostWidget && root.hostWidget.runBenchmark) root.hostWidget.runBenchmark()
  }

  KeyboardPanel {
    id: panel
    anchorItem: root.anchorItem
    owner: root.hostWidget || root
    bar: root.bar
    open: root.opened
    focusTarget: keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(240))
    contentHeight: panel.fittedContentHeight(content.implicitHeight)

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction) }

      Column {
        id: content
        width: parent.width
        spacing: Style.space(8)

        // The click asks what the evidence state is. The word is the answer.
        // Rows, limit sentences, and a control that cannot run stay in the
        // status document and off this surface.
        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          text: {
            var host = root.hostWidget
            var doc = host ? host.document : null
            if (doc && doc.state) return String(doc.state)
            if (host && host.faultState) return String(host.faultState)
            return ""
          }
          color: root.urgent(text) ? root.urgentColor() : root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.displayLarge
          font.bold: root.urgent(text)
        }

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          visible: text !== ""
          text: {
            var host = root.hostWidget
            if (host && host.problem && !host.document) return String(host.problem)
            var doc = host ? host.document : null
            return doc && doc.model ? String(doc.model) : ""
          }
          color: root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.subtitle
        }

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          visible: text !== ""
          text: {
            var doc = root.hostWidget ? root.hostWidget.document : null
            if (!doc || !doc.next) return ""
            return String(doc.next.reason || doc.next.text || "")
          }
          color: root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.subtitle
        }

        Repeater {
          model: root.hostWidget && root.hostWidget.document && root.hostWidget.document.unresolved
            ? root.hostWidget.document.unresolved : []
          delegate: Text {
            required property var modelData
            width: content.width
            wrapMode: Text.WordWrap
            text: {
              var id = String(modelData.id || "")
              var reason = modelData.reason ? String(modelData.reason) : ""
              return reason !== "" ? id + ": " + reason : id
            }
            color: root.urgent(modelData.state) ? root.urgentColor() : root.barForeground
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.subtitle
            font.bold: root.urgent(modelData.state)
          }
        }

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          visible: root.measurementReady()
          text: root.requestMeasurement ? "Confirm" : "Run"
          color: root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.subtitle
          font.underline: true

          MouseArea {
            anchors.fill: parent
            onClicked: root.runRequested()
          }
        }
      }
    }
  }
}
