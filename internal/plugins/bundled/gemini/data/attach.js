// Pastes a picture into the message box of AI Mode, as if it were copied and pasted. Takes the
// image as base64 and its media type; returns "ok" or why it could not.
(async (b64, mime) => {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  const ext = (mime.split('/')[1] || 'png').replace('jpeg', 'jpg');
  const file = new File([bytes], 'image.' + ext, { type: mime });
  const dt = new DataTransfer();
  dt.items.add(file);
  const box = document.activeElement && document.activeElement !== document.body
    ? document.activeElement
    : document.querySelector('textarea, [role=textbox], [role=combobox], [contenteditable=true]');
  if (!box) return 'the message box is not on the page';
  box.focus();
  box.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
  return 'ok';
})