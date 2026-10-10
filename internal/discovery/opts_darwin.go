package discovery

// privileged is false on macOS: unprivileged UDP ICMP sockets need no root.
const privileged = false
