<script setup>
import { cn } from '@/lib/utils'
defineProps({ class: { type: String, default: '' }, align: { type: String, default: 'left' } })
</script>

<template>
  <td
    :class="cn(
      'px-3 py-1.5 align-middle',
      $props.align === 'right' ? 'num' : '',
      $props.class,
    )"
  >
    <slot />
  </td>
</template>
