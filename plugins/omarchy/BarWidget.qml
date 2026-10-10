import QtQuick
import Quickshell
import Quickshell.Io
import qs.Ui

// Omarchy Quattro bar widget. Checked 2026-10-09 against
// plugins.omarchy.org/develop.html (updated 13 Aug 2026),
// shell/services/PluginRegistry.qml, and shell/Ui/BarWidget.qml on quattro.
// The detail panel is loaded here. kinds stays bar-widget. Quickshell
// Process.command is a string list (quickshell-mirror process.hpp,
// 43d4fa9e883cb03239b3d578c9c57070f4fbd281). This file never starts a
// benchmark while it is constructed, and it never executes the status
// document's next action.
BarWidget {
  id: root
  moduleName: "dev.fitr.evidence"

  property var document: null
  property string faultState: ""
  property string problem: ""

  readonly property bool opened: panelLoader.item
    ? panelLoader.item.opened === true
    : false
  readonly property bool popoutSwitchClosing: panelLoader.item
    ? panelLoader.item.popoutSwitchClosing === true
    : false
  readonly property string barText: {
    if (document && document.model)
      return String(document.model)
    if (document && document.state)
      return String(document.state)
    return faultState || "?"
  }

  function open() {
    if (panelLoader.item) panelLoader.item.open()
  }

  function close() {
    if (panelLoader.item) panelLoader.item.close()
  }

  function toggle() {
    if (panelLoader.item) panelLoader.item.toggle()
  }

  function closeForPopoutSwitch() {
    if (panelLoader.item) panelLoader.item.closeForPopoutSwitch()
  }

  function injectPanel() {
    if (!panelLoader.item) return
    panelLoader.item.bar = root.bar
    panelLoader.item.anchorItem = button
    panelLoader.item.hostWidget = root
  }

  function commandToken(value) {
    var text = String(value || "")
    if (text.length < 1 || text.length > 256) return ""
    if (text.indexOf("..") !== -1 || text.indexOf("//") !== -1) return ""
    if (!/^[A-Za-z0-9_./:-]+$/.test(text)) return ""
    return text
  }

  function roleToken(value) {
    var text = String(value || "")
    if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(text)) return ""
    return text
  }

  function refreshStatus() {
    if (statusProcess.running) return
    var bin = commandToken(root.setting("fitrCommand", "fitr"))
    if (!bin) {
      root.failStatus("unsupported", "The fitr command setting is not a single path token.")
      return
    }
    var role = roleToken(root.setting("role", ""))
    if (role)
      statusProcess.command = [bin, "desktop", "status", "--display", "json", "--role", role]
    else
      statusProcess.command = [bin, "desktop", "status", "--display", "json"]
    statusProcess.running = true
  }

  function acceptStatus(text) {
    var parsed
    try {
      parsed = JSON.parse(text)
    } catch (e) {
      root.failStatus("unsupported", "The status output is not a desktop status document.")
      return
    }
    if (!parsed || parsed.schema !== "fitr.desktop.status.v1" || !Array.isArray(parsed.rows)) {
      root.failStatus("unsupported", "The status output is not a desktop status document.")
      return
    }
    root.problem = ""
    root.faultState = ""
    root.document = parsed
  }

  function failStatus(state, reason) {
    root.document = null
    root.faultState = state
    root.problem = reason
  }

  function runBenchmark() {
    if (benchmarkProcess.running) return
    var doc = root.document
    if (!doc || !doc.next || doc.next.effect !== "explicit-local" || doc.next.local_proven !== true) return
    var role = roleToken(doc.role)
    var bin = commandToken(root.setting("fitrCommand", "fitr"))
    if (!role || !bin) return
    benchmarkProcess.command = [bin, "desktop", "benchmark", "--role", role, "--confirm"]
    benchmarkProcess.running = true
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight
  onBarChanged: injectPanel()

  Loader {
    id: panelLoader
    active: true
    source: Qt.resolvedUrl("Panel.qml")
    visible: false
    onLoaded: {
      root.injectPanel()
      Qt.callLater(root.injectPanel)
    }
  }

  Process {
    id: statusProcess
    stdout: StdioCollector {
      id: statusOut
      waitForEnd: true
    }
    onExited: function(exitCode) {
      if (exitCode !== 0) {
        root.failStatus("unavailable", "fitr desktop status did not produce a document.")
        return
      }
      root.acceptStatus(statusOut.text)
    }
  }

  Process {
    id: benchmarkProcess
    stdout: StdioCollector { waitForEnd: true }
    stderr: StdioCollector { waitForEnd: true }
    onExited: function(exitCode) {
      if (panelLoader.item) panelLoader.item.requestMeasurement = false
      root.refreshStatus()
    }
  }

  Timer {
    id: refreshTimer
    interval: 300000
    running: true
    repeat: true
    triggeredOnStart: true
    onTriggered: {
      var seconds = Number(root.setting("refreshIntervalSec", 300))
      if (!isFinite(seconds) || seconds < 60) seconds = 60
      if (seconds > 3600) seconds = 3600
      refreshTimer.interval = seconds * 1000
      root.refreshStatus()
    }
  }

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.barText
    tooltipText: "Open fitr evidence"
    onPressed: function(buttonCode) {
      if (buttonCode === Qt.LeftButton) root.toggle()
    }
  }
}
