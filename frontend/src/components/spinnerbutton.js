import van from "vanjs-core";

const { span, button } = van.tags

// options.base replaces the default shape classes (radius, padding, weight)
// for callers whose own classes would otherwise conflict with them.
export function spinnerButton(text, onClick=undefined, classes='', type='button', disabledWhen=undefined, options={}) {
    const base = options.base ?? 'rounded-lg px-4 py-2 font-medium'
    const disabledClass = options.disabledClass ?? 'opacity-70 cursor-not-allowed'
    const isSubmitting = van.state(false)
    const isDisabled = () => isSubmitting.val || (disabledWhen ? disabledWhen() : false)
    const b = button(
        {
            class: () => `${base} relative transition-colors ${classes} ${
                isDisabled() ? disabledClass : 'cursor-pointer'}`,
            disabled: isDisabled,
            onclick: async (e) => {
                if (!onClick || isDisabled()) return
                isSubmitting.val = true
                try {
                    await onClick?.(e)
                } finally {
                    isSubmitting.val = false
                }
            },
            type: type
        },
        span(
            {class: 'relative inline-flex items-center justify-center w-max'},
            span({
                    class: () => isSubmitting.val ? 'invisible' : ''
                },
                text),
            span({
                    class: () => `absolute w-[1.2em] h-[1.2em] border-[0.15em] border-white/30 border-t-white rounded-full animate-spin ${isSubmitting.val ? '' : 'hidden'}`
            })
        )
    )
    b.isSubmitting = isSubmitting
    return b
}
