#!/usr/bin/env python3
import importlib.util
import pathlib
import socket
import tempfile
import time
import unittest


HERE = pathlib.Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("owned_pod_stream", HERE / "owned-pod-stream.py")
STREAM = importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(STREAM)


def target():
    return {"kubeconfig":"/tmp/kubeconfig","context":"k3d-hakopod-dev","namespace":"managed-platform-a",
            "namespace_uid":"11111111-1111-1111-1111-111111111111","pod":"gateway-abc","pod_uid":"22222222-2222-2222-2222-222222222222",
            "owner_kind":"ReplicaSet","owner_name":"gateway-abc","owner_uid":"33333333-3333-3333-3333-333333333333",
            "workload_kind":"Deployment","workload_name":"supabase-api-gateway","workload_uid":"44444444-4444-4444-4444-444444444444",
            "container":"envoy","image":"example.invalid/envoy@sha256:"+"a"*64,"port":8443}


class OwnedPodStreamTest(unittest.TestCase):
    def test_binary_stream_and_fixed_exec_command(self):
        with tempfile.TemporaryDirectory() as directory:
            fake=pathlib.Path(directory)/"kubectl"
            fake.write_text("#!/usr/bin/env python3\nimport sys\ndata=sys.stdin.buffer.read()\nsys.stdout.buffer.write(data)\nsys.stdout.buffer.flush()\n")
            fake.chmod(0o700)
            relay=STREAM.OwnedPodForward(0,target,2,lifetime=10,idle_timeout=3,stream_timeout=5,kubectl=str(fake)).start()
            try:
                payload=b"\x00\x00\x00\x08\x04\xd2\x16/\x16\x03\x01\x00\x07raw\x00tls"
                client=socket.create_connection(("127.0.0.1",relay.listen_port),timeout=2)
                client.sendall(payload); client.shutdown(socket.SHUT_WR)
                received=b""
                while len(received)<len(payload): received += client.recv(4096)
                self.assertEqual(received,payload)
                client.close()
                deadline=time.monotonic()+3
                while relay.workers and time.monotonic()<deadline: time.sleep(.01)
                self.assertEqual(relay.workers,set())
            finally:
                relay.close()

    def test_rejects_unbounded_connection_count(self):
        with self.assertRaises(RuntimeError): STREAM.OwnedPodForward(0,target,137)

    def test_close_terminates_active_exec_with_one_deadline(self):
        with tempfile.TemporaryDirectory() as directory:
            fake=pathlib.Path(directory)/"kubectl"
            fake.write_text("#!/usr/bin/env python3\nimport time\ntime.sleep(60)\n")
            fake.chmod(0o700)
            relay=STREAM.OwnedPodForward(0,target,2,lifetime=10,kubectl=str(fake)).start()
            client=socket.create_connection(("127.0.0.1",relay.listen_port),timeout=2)
            deadline=time.monotonic()+2
            while not relay.processes and time.monotonic()<deadline: time.sleep(.01)
            started=time.monotonic(); relay.close(); client.close()
            self.assertLess(time.monotonic()-started,7)
            self.assertFalse(relay.processes)

    def test_identity_change_closes_stream(self):
        current=target(); calls=[]
        with tempfile.TemporaryDirectory() as directory:
            fake=pathlib.Path(directory)/"kubectl"
            fake.write_text("#!/usr/bin/env python3\nimport sys\nsys.stdout.buffer.write(sys.stdin.buffer.read())\n")
            fake.chmod(0o700)
            calls=[]
            def callback():
                calls.append(1)
                if len(calls)==2: current["pod_uid"]="55555555-5555-5555-5555-555555555555"
                return dict(current)
            relay=STREAM.OwnedPodForward(0,callback,1,lifetime=10,kubectl=str(fake)).start()
            try:
                client=socket.create_connection(("127.0.0.1",relay.listen_port),timeout=2)
                client.settimeout(3); client.sendall(b"secret")
                try: received=client.recv(1)
                except ConnectionResetError: received=b""
                self.assertEqual(received,b"")
                client.close()
                relay.check_healthy()
                deadline=time.monotonic()+3
                while relay.workers and time.monotonic()<deadline: time.sleep(.01)
                calls.clear()
                second=socket.create_connection(("127.0.0.1",relay.listen_port),timeout=2)
                second.sendall(b"next"); second.shutdown(socket.SHUT_WR)
                try: second_received=second.recv(4)
                except ConnectionResetError: second_received=b""
                relay.check_healthy()
                self.assertEqual(second_received,b"next")
                second.close(); relay.check_healthy()
            finally:
                relay.close()


if __name__ == "__main__": unittest.main()
