import QtQuick
import Quickshell
import Quickshell.Io

Item {
  id: root

  property var shell: null
  property bool stopping: false
  property int restartDelayMs: 2000

  Process {
    id: daemon
    command: ["agentintercom", "start"]
    running: true

    onStarted: healthyTimer.restart()
    onExited: function(exitCode) {
      if (root.stopping) return
      healthyTimer.stop()
      console.warn("Agent Intercom stopped with exit code", exitCode)
      restartTimer.interval = root.restartDelayMs
      root.restartDelayMs = Math.min(root.restartDelayMs * 2, 30000)
      restartTimer.restart()
    }
  }

  Timer {
    id: healthyTimer
    interval: 60000
    repeat: false
    onTriggered: root.restartDelayMs = 2000
  }

  Timer {
    id: restartTimer
    interval: 2000
    repeat: false
    onTriggered: if (!root.stopping) daemon.running = true
  }

  Component.onDestruction: {
    root.stopping = true
    restartTimer.stop()
    healthyTimer.stop()
    daemon.running = false
  }
}
