<script setup>
import { computed } from 'vue'
import { cva } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-primary text-primary-foreground',
        secondary: 'border-transparent bg-secondary text-secondary-foreground',
        outline: 'text-foreground',
        // 会计语义色：借/贷、盈/亏用固定的两种颜色。
        // 用色相而不是深浅来区分，是为了在黑白打印的报表上也分得开。
        debit: 'border-transparent bg-[var(--debit)]/12 text-[var(--debit)]',
        credit: 'border-transparent bg-[var(--credit)]/12 text-[var(--credit)]',
        profit: 'border-transparent bg-[var(--profit)]/12 text-[var(--profit)]',
        loss: 'border-transparent bg-[var(--loss)]/12 text-[var(--loss)]',
        warn: 'border-transparent bg-[var(--warn)]/18 text-[var(--warn)]',
        muted: 'border-transparent bg-muted text-muted-foreground',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

const props = defineProps({ variant: { type: String, default: 'default' } })
const classes = computed(() => cn(badgeVariants({ variant: props.variant })))
</script>

<template>
  <span :class="classes"><slot /></span>
</template>
