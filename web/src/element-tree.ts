import { isValidElement } from "react";
import type { ReactElement, ReactNode } from "react";

type Props = Record<string, unknown>;

/**
 * elementsOf lists every element a tree holds, calling each function component on the way down, so a
 * test can find a control a component draws and call its handler without a DOM. It reaches only
 * components that use no hooks.
 */
export function elementsOf(node: ReactNode): ReactElement<Props>[] {
  if (Array.isArray(node)) {
    return node.flatMap(elementsOf);
  }
  if (!isValidElement<Props>(node)) {
    return [];
  }
  const { type, props } = node;
  const below: ReactNode =
    typeof type === "function" ? Reflect.apply(type, undefined, [props]) : props.children;
  return [node, ...elementsOf(below)];
}

/** press calls the handler an element holds under name, as the browser would on the event it is given. */
export function press(element: ReactElement<Props>, name: string, event: unknown = {}): void {
  const handler = element.props[name];
  if (typeof handler !== "function") {
    throw new Error(`the element has no ${name} handler`);
  }
  handler(event);
}
