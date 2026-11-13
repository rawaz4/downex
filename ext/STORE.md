# Store listings: privacy and permissions (draft)

Text for the Chrome Web Store, Edge Add-ons and addons.mozilla.org (AMO) forms. Every statement was
checked against the extension's source (file references in brackets are for maintainers, not for the
forms). Keep it in step with the code: a change to what the extension reads, stores or sends must
change this file too.

Still needed from the owner before a submission: a public URL where the privacy policy is hosted, the
developer accounts, a homepage/download URL for Downex, screenshots and promotional images, and the
stores' extension IDs after the first upload (they go into `EXTENSION_ID.txt`). The Chrome Web Store
and Edge get the store build (`pnpm zip:store`, `pnpm zip:store:edge`, no YouTube); AMO gets the
default build (`pnpm zip:firefox`). Which store gets which build is the owner's decision. For the first
release Downex is listed on AMO and Edge Add-ons only; Chrome, Brave and Chromium load the unpacked
copy that the installer ships (the Chrome Web Store text below stays for later).

## Single purpose

Send the browser's downloads and the videos on a page to the Downex download manager installed on
this computer.

## Privacy policy (draft)

> **Downex browser extension: privacy policy**
>
> The Downex extension works only with the Downex download manager installed on your own computer.
> It has no servers of its own and sends nothing to the developer of Downex or to anyone else: no
> analytics, no advertising, no tracking and no remote code.
>
> **What the extension hands to Downex.** It talks only to the Downex program on your computer,
> through your browser's native messaging. When you download a file, send a link from the context
> menu, or choose a video or stream, it hands Downex what is needed to download it as your browser
> would:
>
> - the file's address, the request method, the request headers your browser sent for it (these can
>   include your cookies, an `Authorization` header and the referring page), a form body of at most
>   1 MiB, and the file's name, type and size;
> - for a download or a stream detected on a page, the cookies your browser holds for that one
>   address, so a download that needs you to be signed in works in Downex;
> - for a video on a page, the page's address and title. Your cookies are added for a video only
>   when you turn on "Send my sign-in cookies when a video needs it" (off unless you turn it on).
>
> On connecting, the extension also tells Downex which browser it runs in and its own version, so
> that the two agree on how to talk to each other.
>
> In Firefox, the install prompt lists these as browsing activity, website content, website
> activity and authentication information: installing the add-on is agreeing to hand them to the
> Downex program on your computer, and to nothing else.
>
> **Preparing video qualities.** With "Prepare video qualities while you watch" on (it is on unless
> you turn it off), after you stay 1.5 seconds on a video of a supported site, the extension hands the
> page's address (never your cookies) to Downex, which then asks that site for the video's
> qualities, so the Download button lists them at once. You can turn it off in the extension's
> options.
>
> **What the extension keeps in your browser.** Your capture rules and options; the last 10 items it
> handed to Downex; the videos and files detected in your open tabs, with the request headers they
> were loaded with (kept only for the browser session); and a log of its last 20 capture steps, with
> the query part of each address removed. Nothing of this leaves your computer except as described
> above, to Downex.
>
> **Private windows.** The extension does not run in private (incognito) windows, so it never sees
> their downloads, pages or cookies.
>
> **The Downex program.** Downex keeps each download's address and request headers, cookies included,
> in its database on your computer until you delete the download, so it can resume it. It contacts the
> sites you download from, downloads its video tools (yt-dlp and Deno) from GitHub when they are
> first needed, and checks its update feed. It sends your downloads, cookies and browsing to no one.
>
> **Contact.** support@downex.app

Sources: [ext/src/lib/context.ts `PRIORITY_HEADERS`; protocol.ts `MAX_BODY_BYTES`; background.ts
`cookies.getAll` for one URL; options-form.ts `useSignInForVideos: false`, `videoPrefetch: true`;
video-prefetch.ts `PREFETCH_DWELL_MS` and no cookies on a look-ahead; history.ts `HISTORY_LIMIT`;
capture-trace.ts `TRACE_LIMIT` and the query replaced by `?…`; media headers only in
`storage.session`; wxt.config.ts `incognito: 'not_allowed'`; internal/store headers column; tools
download and update feed in the daemon.] The store build has no YouTube video features; the text
above holds for both builds.

## Permission justifications

| Permission | Why |
| --- | --- |
| `downloads` | To take over a download you start in the browser (cancel it there and hand it to Downex) and to leave it to the browser when Downex is not available. |
| `nativeMessaging` | The only channel to the Downex program on this computer; nothing is sent anywhere else. |
| `webRequest` | To read a response's headers to decide whether it is a download, and to notice the video streams and files a page plays so the toolbar menu can list them. Observe-only on Chromium. |
| `webRequestBlocking` (Firefox only) | Firefox's download capture holds a download's response for at most 1.5 seconds and cancels it in the browser once Downex has taken it; otherwise the browser downloads it as usual. |
| `webNavigation` | To clear a tab's detected videos when it moves to another page, and to find which frame of the page a video plays in. |
| `cookies` | To read the cookies of the one address being downloaded, so a download that needs you to be signed in works in Downex. Never read in bulk. |
| `contextMenus` | "Download with Downex" and "Download all links" in the right-click menu. |
| `storage` | Your capture rules and options, the recent hand-offs, and the videos detected in open tabs. |
| `scripting` | To show the Download button and its quality list over the video playing in a page. |
| Host access to `http://*/*` and `https://*/*` | Downloads and videos can come from any website; the extension needs to see those requests and pages wherever you start them. It does nothing on other schemes and never in private windows. |

## Store form answers

- **Remote code:** No. All code is in the package; there is no `eval`, `new Function`, remote script
  or `fetch` to any server.
- **Chrome Web Store, data usage:** declare *Authentication information* (cookies and request headers
  of a download) and *Web history* (the address and title of the page a download or video comes
  from). Certify: not sold to third parties, not used or transferred for purposes unrelated to the
  single purpose, not used for creditworthiness or lending. The data goes only to the Downex program
  on the user's own computer.
- **AMO:** Mozilla's Add-on Policies apply to data sent to a native application as well, so the
  data handed to the local Downex program is declared, although it never leaves the computer. The
  manifest (`wxt.config.ts`) has, under `browser_specific_settings.gecko`:
  - `id: 'extension@downex.app'`, permanent from the first upload: AMO ties the listing and every
    installed copy's updates to it. `downex-nmhost` allows it, and the earlier `browser@downex.local`
    of copies loaded by hand.
  - `strict_min_version: '140.0'`, the first Firefox with the built-in data consent, so no Firefox
    installs the add-on without showing the data below.
  - `data_collection_permissions: { required: [...] }` with Mozilla's data types; Firefox shows them
    in the install prompt and the user accepts them by installing:

    | Type | What is handed to Downex |
    | --- | --- |
    | `browsingActivity` | the address and title of the page a download or video comes from; with "Prepare video qualities while you watch", the address of a video page the user stays on 1.5 s |
    | `websiteContent` | a download's request headers (cookies, `Referer`), its form body (at most 1 MiB), the cookies of that one address |
    | `websiteActivity` | the downloads the user starts in the browser, handed over instead of the browser's |
    | `authenticationInfo` | an `Authorization` header a download was sent with (HTTP Basic carries a user name and password) |

  None is optional: downloads cannot be handed over without them. Not declared, and named in the
  reviewer notes: on every connection the extension's `hello` message tells Downex the browser's
  name and the extension's version (`protocol.ts` `HelloParams`), only to choose the protocol, a
  part of the required function that no data type covers. Should a reviewer count it as technical
  data, the fallback is to stop sending `extVersion` in the Firefox build. The look-ahead ("Prepare video
  qualities while you watch", on unless turned off) sends a page address in the background, which
  the policies allow only with explicit consent: the required `browsingActivity` is that consent,
  given in Firefox's install prompt, and the listing's description and the privacy policy above
  name the look-ahead and its off switch. An optional `browsingActivity` for the look-ahead alone
  would add nothing: Firefox's consent is per data type, and downloads require that type anyway.
  In the submission form, answer that the add-on sends data only to a native application on the
  user's computer, and paste the privacy policy. Notes to the reviewer: "Everything the add-on sends
  goes to the Downex program on this computer through native messaging, never to a server. On
  connecting it tells Downex the browser's name and the add-on's version, only to choose the
  protocol." Mark the listing as desktop only (native messaging
  does not exist on Firefox for Android). AMO needs the sources zip and `README.md`'s build steps.
  Until the listing is live, Downex's instructions offer the temporary `about:debugging` route.
- **Privacy policy URL:** the hosted copy of the policy above (all three stores).
