import { Component, type ReactNode } from "react";
import { ErrorState } from "./ui";

export class RouteBoundary extends Component<{children: ReactNode}, {failed: boolean}> {
  state = {failed: false};
  static getDerivedStateFromError() {return {failed: true};}
  render() {
    return this.state.failed
      ? <ErrorState message="页面未能加载，请刷新后重试。" retry={() => window.location.reload()}/>
      : this.props.children;
  }
}
