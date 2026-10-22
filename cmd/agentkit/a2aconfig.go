package main

// a2aName is the a2a component's name (pkg/a2a's Name), here so that serve
// knows it in a build without the component (-tags noa2a).
const a2aName = "a2a"

// envA2AURL is the URL the A2A Agent Cards name the server at, such as
// https://agents.example.com when a proxy terminates TLS in front of it.
// Unset, a card names the scheme and host of the request that fetched it.
const envA2AURL = "AGENTKIT_A2A_URL"
