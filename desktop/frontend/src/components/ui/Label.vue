<script setup>
defineProps({ for: { type: String, default: '' } })
</script>

<template>
  <label :for="$props.for" class="text-sm font-medium leading-none">
    <slot />
  </label>
</template>
