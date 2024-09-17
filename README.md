# ssh-srv

Resolves an `_ssh._tcp` SRV record, and passes the socket to SSH via ProxyUseFdPass.

By using ProxyUseFdPass, ssh takes ownership of the socket, and this tool isn't used
for proxying

## Installation

```
go install jeremy.visser.name/go/ssh-srv@latest
```

## Synopsis

```
ssh-srv HOSTNAME [PORT]
```

Port is optional, and only used in the case of non-SRV fallback.
If SRV records are found, the port from the SRV is used instead.

## Usage

With SSH options passed on the command line:

```
ssh -o ProxyUseFdPass=yes -o ProxyCommand='ssh-srv %h' user@myserver.mydomain.invalid
```

With ~/.ssh/config:

```
Host *.mydomain.invalid
		ProxyUseFdPass  yes
		ProxyCommand    ssh-srv %h
```

Example SRV records:

```
_ssh._tcp.myserver.mydomain.invalid.  1800  IN SRV  0 0    22   myserver1.mydomain.invalid.
_ssh._tcp.myserver.mydomain.invalid.  1800  IN SRV  1 0  2222   myserver1.mydomain.invalid.
_ssh._tcp.myserver.mydomain.invalid.  1800  IN SRV  2 0    22  myserver2a.mydomain.invalid.
_ssh._tcp.myserver.mydomain.invalid.  1800  IN SRV  2 0    22  myserver2b.mydomain.invalid.
```
