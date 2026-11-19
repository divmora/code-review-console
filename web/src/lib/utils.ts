import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// cn merges class names, letting later Tailwind classes override earlier ones.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
