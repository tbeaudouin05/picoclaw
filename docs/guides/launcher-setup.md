# Launcher Setup Guide

This guide explains how to make the PicoClaw launcher reachable from your local machine when the launcher process is running on another host.

## Current status

Today, the macOS app and other desktop clients expect the launcher to already be reachable locally, typically at:

- `http://127.0.0.1:18800`

That means **you must currently create the SSH tunnel or other port-forward manually** before the app can connect.

## Intended future experience

The intended future desktop UX is simpler:

- the app collects the required SSH connection details
- the app creates and manages the tunnel automatically
- the user does not need to start the tunnel in a separate terminal

Until that is implemented, use one of the manual approaches below.

## Manual SSH tunnel setup

If the launcher is already listening on `127.0.0.1:18800` on a remote machine that you can SSH into, forward it to your local machine like this:

```bash
ssh -N -L 18800:127.0.0.1:18800 <user>@<remote-host>
```

Then open:

- `http://127.0.0.1:18800`

### What the command does

- local `18800` on your machine forwards to
- remote `127.0.0.1:18800` on the SSH host

If the launcher is listening on a different remote port, replace the right-hand port accordingly.

## Verify that the tunnel is working

After starting the tunnel, verify that something is reachable locally:

```bash
curl -i http://127.0.0.1:18800
```

Or check whether something is listening on that port:

```bash
lsof -i tcp:18800
```

If you still see connection errors, confirm that:

- the launcher process is actually running on the remote host
- the remote launcher is listening on the expected host and port
- your SSH credentials and host are correct
- your local machine is not already using port `18800`

## For app developers and testers

If a desktop client reports `ERR_CONNECTION_REFUSED` for `127.0.0.1:18800`, the problem is usually that no local tunnel or local launcher process exists yet. The UI may be working correctly; the missing reachable launcher endpoint is the real blocker.

## Related docs

- [Docker & Quick Start Guide](docker.md)
- [Configuration Guide](configuration.md#web-launcher-dashboard)
