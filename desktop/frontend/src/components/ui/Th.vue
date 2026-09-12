<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: String, default: '' }, align: { type: String, default: 'left' } })
</script>

<template>
  <th
    :class="cn(
      'h-9 px-3 align-middle text-xs font-medium text-muted-foreground whitespace-nowrap',
      $props.align === 'right' ? 'text-right' : 'text-left',
      $props.class,
    )"
  >
    <slot />
  </th>
</template>
