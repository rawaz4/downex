# Security

## Supported versions

Only the latest release of Downex gets security fixes. Downex tells you in its window when a
newer version is available; please update before you report a problem.

## Reporting a problem

Please report security problems privately, not in a public issue:

1. Open [github.com/rawaz4/downex/security](https://github.com/rawaz4/downex/security).
2. Choose **Report a vulnerability** and describe the problem: the Downex version and system, what
   an attacker can do, and the steps to reproduce it.

If that button is missing, write to [support@downex.app](mailto:support@downex.app) instead,
with "Security" in the subject.

The report is seen only by the maintainers. You will get an answer there, and the fix is
published in a new release before the details are made public.

## What counts

Downex runs on your own computer, so the most important problems are those where something
outside your account can make Downex act: a web page, a downloaded file, a server, a stream or
video page, a proxy, an update or another user on the same computer. Examples are a way to write
outside the chosen download folder, to reach the background service from another account or
from the network, to get cookies or passwords to the wrong place, or to install an update that
Downex's signature check should have refused.
