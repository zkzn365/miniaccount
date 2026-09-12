<script setup>
import { computed } from 'vue'
import { cva } from 'class-variance-authority'
import { cn } from '@/lib/utils'

// 与 shadcn-vue 的 Button 变体保持一致：同一套变体名（default/secondary/
// destructive/outline/ghost/link）与尺寸名，社区示例可以直接套用。
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: 'bg-primary text-primary-foreground hover:bg-primary/90',
        destructive: 'bg-destructive text-destructive-foreground hover:bg-destructive/90',
        outline: 'border border-input bg-background hover:bg-accent hover:text-accent-foreground',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
        ghost: 'hover:bg-accent hover:text-accent-foreground',
        link: 'text-primary underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-4 py-2',
        sm: 'h-8 rounded-md px-3 text-xs',
        lg: 'h-10 rounded-md px-6',
        icon: 'h-9 w-9',
      },
    },
    defaultVariants: { variant: 'default', size: 'default' },
  },
)

const props = defineProps({
  variant: { type: String, default: 'default' },
  size: { type: String, default: 'default' },
  type: { type: String, default: 'button' },
  disabled: Boolean,
})
const classes = computed(() => cn(buttonVariants({ variant: props.variant, size: props.size })))
</script>

<template>
  <button :type="type" :disabled="disabled" :class="classes">
    <slot />
  </button>
</template>
