#!/usr/bin/env python3
"""Verify negotiated compression and lossless large input/output on loopback."""
import base64
import hashlib
import json
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time
import zlib


def read_exact(read, size):
    result = bytearray()
    while len(result) < size:
        data = read(size - len(result))
        if not data:
            raise EOFError('Unexpected EOF')
        result.extend(data)
    return bytes(result)


def worker():
    import tty
    tty.setraw(0)
    os.write(1, b'READY')
    size = struct.unpack('!I', read_exact(lambda n: os.read(0, n), 4))[0]
    data = read_exact(lambda n: os.read(0, n), size)
    while data:
        data = data[os.write(1, data):]
    time.sleep(1)


class WebSocket:
    def __init__(self, port):
        self.sock = socket.create_connection(('127.0.0.1', port), timeout=10)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f'GET /ws HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n'
            'Upgrade: websocket\r\nConnection: Upgrade\r\n'
            f'Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n'
            'Sec-WebSocket-Protocol: tty\r\n'
            'Sec-WebSocket-Extensions: permessage-deflate\r\n\r\n'
        )
        self.sock.sendall(request.encode())
        header = bytearray()
        while not header.endswith(b'\r\n\r\n'):
            header.extend(read_exact(self.sock.recv, 1))
            if len(header) > 16384:
                raise AssertionError('Oversized upgrade response')
        expected = base64.b64encode(hashlib.sha1(
            (key + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest())
        assert header.startswith(b'HTTP/1.1 101 '), header
        assert expected in header, 'Invalid upgrade response'
        assert b'Sec-WebSocket-Extensions: permessage-deflate' in header, 'Compression unavailable'
        self.inflater = zlib.decompressobj(-15)
        self.deflater = zlib.compressobj(wbits=-15)
        self.compressed_messages = 0

    def send(self, data, opcode=2, compress=False):
        first = 0x80 | opcode
        if compress:
            data = self.deflater.compress(data) + self.deflater.flush(zlib.Z_SYNC_FLUSH)
            assert data.endswith(b'\x00\x00\xff\xff')
            data = data[:-4]
            first |= 0x40
        n = len(data)
        length = bytes([0x80 | n]) if n < 126 else (
            b'\xfe' + struct.pack('!H', n) if n < 65536 else b'\xff' + struct.pack('!Q', n))
        mask = os.urandom(4)
        self.sock.sendall(bytes([first]) + length + mask +
                          bytes(v ^ mask[i % 4] for i, v in enumerate(data)))

    def receive(self):
        payload = bytearray()
        compressed = False
        while True:
            a, b = read_exact(self.sock.recv, 2)
            n = b & 127
            if n == 126:
                n = struct.unpack('!H', read_exact(self.sock.recv, 2))[0]
            elif n == 127:
                n = struct.unpack('!Q', read_exact(self.sock.recv, 8))[0]
            assert n <= 4 * 1024 * 1024, 'Oversized WebSocket frame'
            assert not b & 128, 'Server frame must not be masked'
            data = read_exact(self.sock.recv, n)
            opcode = a & 15
            if opcode == 9:
                self.send(data, 10)
                continue
            if opcode == 8:
                raise EOFError('Server closed during transfer')
            if opcode == 10:
                continue
            if opcode in (1, 2):
                compressed = bool(a & 64)
            payload.extend(data)
            if a & 128:
                if compressed:
                    self.compressed_messages += 1
                    return self.inflater.decompress(bytes(payload) + b'\x00\x00\xff\xff')
                return bytes(payload)

    def output(self, size):
        result = bytearray()
        while len(result) < size:
            message = self.receive()
            if message[:1] == b'0':
                result.extend(message[1:])
        assert len(result) == size
        return bytes(result)

    def close(self):
        try:
            self.send(struct.pack('!H', 1000), 8)
        finally:
            self.sock.close()


def check(binary):
    with socket.socket() as reservation:
        reservation.bind(('127.0.0.1', 0))
        port = reservation.getsockname()[1]
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen(
            [binary, '-W', '-i', '127.0.0.1', '-p', str(port), '--',
             sys.executable, os.path.abspath(__file__), '--worker'],
            stdout=log, stderr=log)
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError('ttyd exited during startup')
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=.1):
                        break
                except OSError:
                    time.sleep(.05)
            for size, compress in [(8192, True), (1048576, True), (65536, False)]:
                ws = WebSocket(port)
                try:
                    ws.send(json.dumps({'AuthToken': '', 'columns': 160, 'rows': 50}).encode())
                    assert ws.output(5) == b'READY'
                    unit = b'Long paste integrity: \xe4\xb8\xad\xe6\x96\x87 0123456789\n'
                    data = (unit * (size // len(unit) + 1))[:size]
                    ws.send(b'0' + struct.pack('!I', size) + data, compress=compress)
                    assert ws.output(size) == data, 'Input/output content changed'
                    assert ws.compressed_messages > 0, 'Server did not compress output'
                    print(f'PASS {size} bytes, compressed input={compress}, exact round trip')
                finally:
                    ws.close()
        except Exception:
            log.seek(0)
            sys.stderr.write(log.read().decode(errors='replace')[-4000:])
            raise
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == '__main__':
    if sys.argv[1:] == ['--worker']:
        worker()
    elif len(sys.argv) == 2:
        check(sys.argv[1])
    else:
        raise SystemExit('Usage: check-ttyd-compression.py /path/to/ttyd')
