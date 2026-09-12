import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** shadcn-vue 的标准类名合并工具。 */
export function cn(...inputs) {
  return twMerge(clsx(inputs))
}
