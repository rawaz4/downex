// Typed requests from extension pages (popup, confirmation page) to the background.

import { browser } from 'wxt/browser';
import type { ClickMessage, ExtensionMessage, ResponseFor } from './messages';

type PageMessage = Exclude<ExtensionMessage, ClickMessage>;

export async function ask<M extends PageMessage>(msg: M): Promise<ResponseFor[M['type']]> {
  const response: unknown = await browser.runtime.sendMessage(msg);
  if (response === undefined) throw new Error('The background did not answer.');
  return response as ResponseFor[M['type']];
}
