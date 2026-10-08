package api

// The fixture keeps a real Python globals dictionary behind one local socket.
// These commands become immutable image/template input before the API starts.
const sessionPythonWorker = `import contextlib,json,os,socket,sys
path='/workspace/kernel.sock'
try: os.unlink(path)
except FileNotFoundError: pass
listener=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
listener.bind(path)
listener.listen(1)
namespace={'__name__':'__main__'}
while True:
 connection,_=listener.accept()
 try:
  raw=bytearray()
  while True:
   chunk=connection.recv(16384)
   if not chunk: break
   raw.extend(chunk)
   if len(raw)>50331648: raise ValueError('input limit')
  request=json.loads(raw)
  if request.get('op')!='exec': raise ValueError('unsupported fixture operation')
  def emit(value): connection.sendall((json.dumps(value)+'\n').encode())
  class Output:
   def write(self,value):
    if value: emit({'status':'output','stdout':value})
    return len(value)
   def flush(self): pass
  try:
   with contextlib.redirect_stdout(Output()),contextlib.redirect_stderr(Output()):
    exec(compile(request['code'],'<notebook>','exec'),namespace,namespace)
   emit({'status':'ok','stdout':''})
  except BaseException as error:
   emit({'status':'error','stdout':'','error':type(error).__name__})
 except (OSError,ValueError,KeyError): pass
 finally: connection.close()
`

const sessionPythonHelper = `import socket,sys
connection=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
connection.connect('/workspace/kernel.sock')
connection.sendall(sys.stdin.buffer.read(50331649))
connection.shutdown(socket.SHUT_WR)
while True:
 chunk=connection.recv(16384)
 if not chunk: break
 sys.stdout.buffer.write(chunk)
 sys.stdout.buffer.flush()
connection.close()
`

const sessionPythonReady = `import socket
probe=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
probe.settimeout(1)
probe.connect('/workspace/kernel.sock')
probe.close()
`
