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
    contentWidth: panel.fittedContentWidth(Style.space(320))
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

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          text: root.hostWidget && root.hostWidget.problem && !root.hostWidget.document
            ? root.hostWidget.problem
            : "fitr evidence"
          color: root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.subtitle
          font.bold: true
        }

        Repeater {
          model: root.hostWidget && root.hostWidget.document && root.hostWidget.document.rows
            ? root.hostWidget.document.rows : []
          delegate: Text {
            required property var modelData
            width: content.width
            wrapMode: Text.WordWrap
            text: String(modelData.label || "") + ": " + String(modelData.value || "") + " (" + String(modelData.state || "") + ")"
            color: root.urgent(modelData.state) ? "#c45c26" : root.barForeground
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.subtitle
          }
        }

        Repeater {
          model: root.hostWidget && root.hostWidget.document && root.hostWidget.document.unresolved
            ? root.hostWidget.document.unresolved : []
          delegate: Text {
            required property var modelData
            width: content.width
            wrapMode: Text.WordWrap
            text: "Unresolved " + String(modelData.id || "") + ": " + String(modelData.state || "")
              + (modelData.reason ? " " + String(modelData.reason) : "")
            color: root.urgent(modelData.state) ? "#c45c26" : root.barForeground
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.subtitle
          }
        }

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          color: root.barForeground
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
          font.pixelSize: Style.font.subtitle
          text: {
            var doc = root.hostWidget ? root.hostWidget.document : null
            if (!doc || !doc.next) return "Opening this panel does not download models, reconfigure serving, or call a model."
            return String(doc.next.reason || doc.next.text || "No next action is recorded.")
          }
        }

        Repeater {
          model: root.hostWidget && root.hostWidget.document && root.hostWidget.document.limits
            ? root.hostWidget.document.limits : []
          delegate: Text {
            required property string modelData
            width: content.width
            wrapMode: Text.WordWrap
            text: modelData
            color: root.barForeground
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            font.pixelSize: Style.font.subtitle
          }
        }

        Text {
          width: parent.width
          wrapMode: Text.WordWrap
          text: !root.requestMeasurement ? "Request measurement"
            : (root.measurementReady() ? "Run local measurement" : "Measurement stays displayed. Locality is not proven.")
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
