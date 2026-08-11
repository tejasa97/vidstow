const FOCUSABLE_SELECTOR = [
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'a[href]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

/** Keeps keyboard focus inside a mounted modal and restores it on close. */
export function trapModalFocus(node: HTMLElement): { destroy(): void } {
  let destroyed = false;
  const previouslyFocused = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : undefined;

  function focusableElements(): HTMLElement[] {
    return Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
      .filter((element) => !element.hasAttribute('disabled') && element.getAttribute('aria-hidden') !== 'true');
  }

  function focusInitialElement(): void {
    if (destroyed || !node.isConnected) return;
    const preferred = node.querySelector<HTMLElement>('[data-autofocus]:not([disabled])');
    (preferred ?? focusableElements()[0] ?? node).focus();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Tab') return;
    const elements = focusableElements();
    if (elements.length === 0) {
      event.preventDefault();
      node.focus();
      return;
    }
    const first = elements[0];
    const last = elements[elements.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function onFocusIn(event: FocusEvent): void {
    if (!destroyed && event.target instanceof Node && !node.contains(event.target)) {
      focusInitialElement();
    }
  }

  node.addEventListener('keydown', onKeydown);
  document.addEventListener('focusin', onFocusIn);
  queueMicrotask(focusInitialElement);

  return {
    destroy() {
      destroyed = true;
      node.removeEventListener('keydown', onKeydown);
      document.removeEventListener('focusin', onFocusIn);
      if (previouslyFocused?.isConnected) previouslyFocused.focus();
    },
  };
}
