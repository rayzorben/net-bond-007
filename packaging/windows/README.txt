Net-Bond-007 for Windows
===================

Double-click net-bond-007.exe and approve the administrator prompt. Net-Bond-007 installs
itself as a Windows service (it starts automatically at boot) and opens the
dashboard at http://127.0.0.1:7007.

Command line (from an administrator prompt):
  net-bond-007.exe install --server bond.example.com:7007 --token YOURTOKEN
  net-bond-007.exe uninstall
  net-bond-007.exe run          (run in the foreground instead of as a service)

Settings and logs: C:\ProgramData\Net-Bond-007\
The Wintun driver (wintun.dll, signed by WireGuard LLC) is embedded in the exe.
