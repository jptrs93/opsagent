import { Capi } from './capi.js'
import { decodeApiErr } from './model.js'

async function handleError(response) {
  let message = `Request failed with HTTP ${response.status}`
  try {
    const apiError = decodeApiErr(await response.arrayBuffer())
    message = apiError.displayErr || message
  } catch {
    // Keep the status-based message when the response is not protobuf.
  }
  throw new Error(message)
}

export const capi = new Capi('', null, handleError)
