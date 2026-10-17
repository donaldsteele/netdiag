package discovery

// privileged selects pro-bing's "privileged" mode. Despite the name, on
// Windows this opens a raw IPv4 ICMP socket (x/net/icmp.ListenPacket with
// "ip4:icmp", bound to 0.0.0.0) and reads the echo replies itself; it does
// not use the IcmpSendEcho (ICMP helper) API. Windows lets a standard user,
// or an administrator's non-elevated process, open a raw ICMP socket, so no
// administrator rights are needed. pro-bing's unprivileged mode (a UDP ICMP
// socket) does not exist on Windows: socket creation fails with "The
// requested protocol has not been configured into the system".
const privileged = true
