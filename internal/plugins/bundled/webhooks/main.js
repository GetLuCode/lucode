// Webhooks: a flow node that posts to one incoming webhook. The address is a secret: it is read
// from the settings on the hub, never shown again, and never written to a log or an error.
"use strict";

// The services this plugin speaks to, which are also the only hosts of its http permission. Each
// names the path its webhooks live under and how it wants its message. Keep this in step with
// plugin.json: a host here that is not there would be refused anyway by lucode.http.
const SERVICES = {
  "hooks.slack.com": {
    name: "Slack",
    path: "/services/",
    limit: 3000,
    // Slack reads <...> as links and mentions, so a card's title must not write them.
    body: text => ({
      text: text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"),
    }),
  },
  "discord.com": {
    name: "Discord",
    path: "/api/webhooks/",
    limit: 2000,
    body: text => ({ content: text, allowed_mentions: { parse: [] } }),
  },
};

// The address, checked by hand against the list above before anything is sent. The errors name
// the host at most: the rest of the address is the secret.
function serviceFor(address) {
  const m = /^https:\/\/([^/?#]+)(\/[^?#]*)(\?[^#]*)?$/i.exec(address);
  if (!m) throw new Error("The webhook address must be a full https address.");
  const host = m[1].toLowerCase();
  const service = Object.prototype.hasOwnProperty.call(SERVICES, host) ? SERVICES[host] : null;
  if (!service) {
    throw new Error("The webhook address points to " + (host.indexOf("@") >= 0 ? "a host with a user in it" : host) + ", and this plugin only sends to " + Object.keys(SERVICES).join(" and ") + ".");
  }
  if (m[2].indexOf(service.path) !== 0 || m[2].indexOf("..") >= 0) {
    throw new Error("That is not a " + service.name + " webhook address: it should start with https://" + host + service.path);
  }
  return service;
}

function send(input) {
  const address = String(lucode.settings.get("webhook_url") || "").trim();
  if (!address) throw new Error("No webhook address yet: set it in Settings, Plugins, Webhooks.");
  const service = serviceFor(address);

  const message = String((input.config && input.config.message) || "").trim();
  if (!message) throw new Error("The message is empty, so nothing was sent.");
  const text = message.length > service.limit ? message.slice(0, service.limit - 1) + "…" : message;

  let res;
  try {
    res = lucode.http.fetch(address, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(service.body(text)),
      timeoutMs: 20000,
    });
  } catch (e) {
    throw new Error(String((e && e.message) || e).split(address).join("the webhook address"));
  }
  if (res.status < 200 || res.status >= 300) {
    const why = res.status === 404 ? "the webhook no longer exists"
      : res.status === 429 ? "too many messages, it will take new ones in a while"
      : "it answered " + res.status;
    throw new Error(service.name + " did not take the message: " + why + ".");
  }
  return { port: "sent", output: { service: service.name, status: res.status } };
}

module.exports = {
  nodes: { send },
};
