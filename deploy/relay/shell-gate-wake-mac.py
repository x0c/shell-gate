#!/usr/bin/python3
"""经家庭网唤醒 Mac（示例：中继主机侧）；仅供 SSH forced-command 调用。"""

import os
from pathlib import Path
import socket
import subprocess
import time


MAC = bytes.fromhex(os.environ["SHELL_GATE_WAKE_MAC"].replace(":", ""))
if len(MAC) != 6:
    raise ValueError("SHELL_GATE_WAKE_MAC must be a six-byte MAC address")
BROADCAST = os.environ["SHELL_GATE_WAKE_BROADCAST"]


def send_magic_packet() -> None:
    packet = b"\xff" * 6 + MAC * 16
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
        for _ in range(5):
            sock.sendto(packet, (BROADCAST, 9))
            time.sleep(0.15)


def nudge_over_lan_ssh() -> bool:
    # Apple Silicon + Wi-Fi 合盖深睡时，单独的魔术包偶发无效；访问已共享的 SSH
    # 服务可经家庭网的 Wake for network access 路径把系统真正唤醒。
    result = subprocess.run(
        [
            "/usr/bin/ssh",
            "-F",
            str(Path.home() / ".ssh/config"),
            "-o",
            "BatchMode=yes",
            "-o",
            "ConnectTimeout=5",
            "-o",
            "ConnectionAttempts=1",
            os.environ["SHELL_GATE_WAKE_SSH_HOST"],
            "/usr/bin/true",
        ],
        check=False,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def main() -> None:
    for attempt in range(3):
        send_magic_packet()
        if nudge_over_lan_ssh():
            return
        if attempt < 2:
            time.sleep(1)


if __name__ == "__main__":
    main()
